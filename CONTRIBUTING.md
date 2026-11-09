# Contributing

## Setup

- Go version: whatever `go.mod` says (currently 1.27).
- [golangci-lint](https://golangci-lint.run) at the exact version `GOLANGCI_LINT_VERSION` in the `Makefile` (CI installs the same one; `make lint` refuses any other). Config in `.golangci.yml`, including the cyclomatic complexity cap of 15.
- For `make test-release`: [syft](https://github.com/anchore/syft) on `PATH` (CI pins the version). GoReleaser is installed into `bin/` at `GORELEASER_VERSION`.
- For `make docs-lint`: Node.js (`npx` fetches markdownlint-cli2 at the pinned `MARKDOWNLINT_CLI2_VERSION`) and [Vale](https://vale.sh) at the exact `VALE_VERSION` in the `Makefile`.
- For `make docs-site`: Python 3.12 and the hash-pinned tools, `pip install --require-hashes -r docs/requirements.txt` (regenerate that file from `docs/requirements.in`, command in its header). It also builds the home page's "Try it" terminal with Go (`make docs-wasm`).
- For `make docs-demo-smoke`: Node.js and Chrome or Chromium (`CHROME=<path>` picks one).
- For `make test-acceptance` (and `ci-e2e`): [uv](https://docs.astral.sh/uv/) on `PATH` (CI pins the version) and network access to PyPI, for `examples/uv-monorepo`. The test fails without uv instead of skipping, so a passing e2e result always includes it.

```sh
make build   # bin/aval
make ci      # every gate CI runs; run it before every push
make lint    # go vet + golangci-lint
make fmt     # gofmt -w .
make docs-lint  # markdownlint + vale over every Markdown file
make docs-cli   # regenerate docs/cli-reference.md after changing a command or flag
make docs-site  # strict build of the MkDocs site into site/; preview with `mkdocs serve`
make docs-demo-smoke  # the "Try it" terminal in headless Chrome; SCREENSHOTS=<dir> saves screenshots
```

## Testing

Every test belongs to exactly one level, chosen by what it proves. Each level
has one `make` target:

| Level | Proves | Lives in | Target |
|---|---|---|---|
| 1 Unit | one package in isolation | beside the code | `make test-unit` (`make test`) |
| 2 Integration | real components wired together, nothing over a reachable network | `tests/integration/` | `make test-integration` |
| 3 Architecture | rules about the repository itself | `tests/architecture/` | `make test-architecture` |
| 4 Local acceptance | a whole user story through the `aval` binary, no cluster | `tests/acceptance/` | `make test-acceptance` |
| 5 Release | the release produces what it claims | `tests/release/` | `make test-release` |

**Choosing a home for a new test.** Ask, in order:

1. **Is it a rule about the repository rather than the product?** ("`pkg/evidence`
   does not import `internal/`", "every CI job is aggregated") → level 3,
   `tests/architecture/`.
2. **Is it about the release artifacts?** → level 5, `tests/release/`.
3. **Can one package prove it?** → level 1, beside the code. Prefer this.
4. **Does it need several real components (contract loader and fingerprint
   engine, an in-process registry), but nothing over a reachable network?** →
   level 2, `tests/integration/`.
5. **Is it a whole user story through the CLI?** → level 4, `tests/acceptance/`.

Two more rules once you have picked:

- **A semantic assertion goes in Go**, not in shell or workflow YAML. Shell
  stays on thin process orchestration.
- **Changes to fingerprinting, contract parsing, verification or policy come
  with false-hit cases**: a relevant change must never produce a reusable
  result.

### False-hit suite

`TestFalseHitCorpus` applies every known kind of change to one base project
and asserts its outcome: a relevant change (content, rename, add, delete,
exec bit, lockfile, toolchain, env value or presence, command, any contract
field, re-included excludes) changes the fingerprint; an irrelevant one
(out-of-scope or excluded files, `.git`, Aval's own outputs, checkout path, ordering,
duplicates, timestamps, non-exec permission bits, YAML formatting) does not;
an unprovable one (see "Fail-closed errors" in [docs/contract.md](docs/contract.md#fail-closed-errors)) fails closed; a repushed
image tag (in a compose file or in `runner.command`) keeps the fingerprint
but is reported as an unacknowledged mutable reference, so it is refused.
The scanner has its own false-hit table per ecosystem, mutable and pinned
variants, in `internal/pinning`, and a fuzz target over every detector. The test fails if
a manifest section is changed by no differ row, so a new section must come
with corpus cases; a new attribute in an existing section or a new contract
field needs its row added by review.

`TestResolveFalseHits` runs the same outcomes through the real resolvers
and in-process fakes: a republished download or a repushed image tag in
`scope.resolve` changes the fingerprint, reordering or repeating entries
does not, and an unresolvable reference fails closed; `internal/resolve`
has the per-ecosystem tables. `TestResolveManifestGolden` pins the
`resolve` lines in `testdata/golden/resolve-aval-fingerprint-vN.manifest`.

`TestContractsFalseHits` and `TestContractsFailClosed` cover
`scope.contracts`: a change inside a referenced contract's scope (also two
levels down) changes the dependent's fingerprint, a change outside it does
not, and a missing, invalid, escaping, symlinked, cyclic or too deep
reference fails closed. `TestContractsFindings` checks that a referenced
contract's mutable references block the dependent. `TestContractsManifestGolden`
pins the `contract <path>` lines in
`testdata/golden/contracts-aval-fingerprint-vN.manifest`.

`TestManifestGolden` pins, per manifest version, the manifests of three
contracts over a fixed tree (`internal/fingerprint/testdata/golden/tree`),
concatenated in `testdata/golden/aval-fingerprint-vN.manifest`. One
contract sets every field: `source`, `exclude`, `dependencies` and `env`
each unsorted and with a repeat, a toolchain map with six keys, argv with
`&`, `<`, `>` and non-ASCII characters, and env vars that are set, set to
the empty string and unset. One omits every optional field; one sets every
optional field to an empty list or map and must encode like the one that
omits them (`TestGoldenOmittedEqualsEmpty`). The tree has a nested path, a
path with a space, a file that sorts differently by full path than in walk
order (`pkg.go` before `pkg/...`), an empty file under a dot-directory,
content with CRLF line endings, an excluded file and a file outside the
scope. The test adds a top-level and a nested `.git` directory (skipped)
and a regular file named `.git` (in scope), and fixes the env values and
the file modes: one exec bit each for owner (0744), group (0654) and other
(0645), all three (0755), and a file with setuid, setgid and sticky but no
exec bit. Any change to how these manifests are built (their lines, the
hashes in them or the canonical contract JSON) fails it unless the header
version is bumped too, so an old fingerprint can never mean different
inputs.

What this covers was measured by mutation: each conditional, constant,
format string and normalization in `Compute`, `canonicalContract`,
`listFiles`, `hashMatching`, `hashFile`, `sum` and the glob matcher was
mutated and `make ci-false-hit` run; every mutant that changes the manifest
of some input fails it, through the golden, the corpus, the glob tables or
the fail-closed tests. The two mutants that pass are equivalent on the
platforms CI runs: dropping `filepath.ToSlash` (the separator is already
`/` on Unix) and masking with `Mode().Perm()&0o111` instead of
`Mode()&0o111`. A path the tree does not exercise (a new manifest feature)
is caught only by the corpus, so it must come with a case in the golden
tree.

`TestGoldensAppendOnly` stops the golden being rewritten to match: a
committed `*.manifest` golden may be added (a new version) but never
modified, renamed or deleted. It diffs the goldens against the merge base of
`HEAD` and `AVAL_GOLDEN_BASE`; in CI that is the PR base branch or the commit
before the push, and the test fails if it does not resolve (after a
force-push to `main` the old commit may be unreachable, and the job fails
until the next normal push). Locally `make ci-false-hit` sets it to
`origin/main` when it is unset, and the test fails when that ref is missing,
so set `AVAL_GOLDEN_BASE` to the upstream main if your remote is named
differently. A plain `go test` without `AVAL_GOLDEN_BASE` (the `ci-engine`
job and the dogfood suite, which check out shallow) skips this one test;
the `ci-false-hit` leg never does. `TestGoldenEditsCatchesEdits` is
its negative test; by hand, append a line to the golden and run
`AVAL_GOLDEN_BASE=HEAD go test ./internal/fingerprint -run TestGoldensAppendOnly`.
The `ci-false-hit` CI job (`make ci-false-hit`, part of `make ci`) runs these
tests and the glob tables on every PR and push, with no path filter.
`TestFalseHitGateIsBinding` (`tests/architecture`) fails if that job or leg
is removed, made conditional, allowed to fail (`continue-on-error`), checked
out shallow, or given any other `AVAL_GOLDEN_BASE`, or if the recipe of
`make ci-false-hit` changes in any way (a `-skip`, a dropped package, a
dropped `-count=1`, a dropped `AVAL_GOLDEN_BASE` default). Edits to the tests themselves are left to review.

### Golden files

`pkg/evidence/testdata/statement-execution-v2.golden.json`,
`pkg/evidence/testdata/verify-report-v1.golden.json`,
`pkg/evidence/testdata/run-record-v1.golden.json` and
`pkg/evidence/testdata/stats-report-v1.golden.json` set every field of the
statement, predicate, verify report, run record and stats report. A test decodes each strictly into the
current types and requires the same JSON back, so dropping, renaming or
retyping a field, or adding a required one, fails. Another test requires
every JSON field of those types to be set in its golden file, so a new field
fails until it is added there. Golden files only grow: an existing field or
value is never removed or changed, an additive change adds its new field to
the golden file of its version, and a new version adds a new file next to
the old ones.

### Go API snapshot

The `pkg/evidence` compatibility rule ([docs/integrations.md](docs/integrations.md#go-api-compatibility))
is enforced. The exported surface is snapshotted in
[`pkg/evidence/testdata/api-v1.txt`](https://github.com/TrianaLab/aval/blob/main/pkg/evidence/testdata/api-v1.txt),
named after the major version in `go.mod`: one line per exported constant,
variable, function, type, field (embedded fields included, with its struct
tag: a changed JSON name or `omitempty` is breaking) and method, with types,
signatures, type parameters and constraints and without parameter names
(type parameter names and the order of union terms are kept, so changing
them needs a snapshot change too). Fields promoted through embedding get a
`promoted <Type>.<Field>` line, so one that becomes ambiguous or changes
type is breaking. Methods are the full method set, promoted ones included,
with the receiver (`T` or `*T`) they need. Interfaces are one line with
their whole method set or type set. A struct that supports `==` also gets a
`comparable <Type>` line. A constant of a type the package declares
(`TrustLevel`, `Result`, `Outcome`) is rendered with its value, since
consumers compare those values: policy files write trust levels by name,
but verify reports and predicates carry them as numbers (the mapping is in
[docs/trust-model.md](docs/trust-model.md#trust-levels)), so renumbering `TrustRunner`
is breaking. Untyped constants (the format
identifiers) are rendered without their value: those values are format
identifiers, governed by the format rules rather than by the snapshot.

`make ci-gates` (`TestEvidenceAPIIsAppendOnly` in `tests/architecture`) fails
when:

- a line of the snapshot no longer matches the code;
- the code exports something the snapshot lacks (an additive change appends
  its lines in the same pull request);
- the exported API names an unexported type of the package (an embedded,
  returned or field type), whose members would not be rendered;
- a non-test file of `pkg/evidence` has any build constraint: a
  `//go:build` or `// +build` line (GOOS, GOARCH, cgo, a `go1.N` release tag
  or a custom tag), `import "C"`, or a `_GOOS`/`_GOARCH` file name suffix.
  The snapshot is taken on one platform and toolchain, and consumers build
  the library on any, so every file must be built everywhere;
- a line of the snapshot at the base revision is missing from the current
  snapshot, so a break cannot be hidden by editing both. The base is the merge
  base with `origin/<base branch>` on a pull request, the commit `main`
  pointed to before a push to `main` (`before` of the push event, so all the
  commits of one push are compared together), and the merge base with
  `origin/main` otherwise. In CI
  (`CI` set) an unresolvable base fails the job; locally, without
  `origin/main`, this one comparison is skipped with a log line.

A new major module path starts a new snapshot file, which has no base to
compare with.

A release with a new major module path is tagged v2.0.0 or later (Go rejects
a `/v2` module tagged v0 or v1), so its changeset sets the version explicitly
rather than relying on a `major` bump from 0.x.

### CI quality gates

`make ci` runs these legs; each is one job of the same name in
`.github/workflows/ci.yaml`:

| Leg | What it checks |
|---|---|
| `ci-static` | gofmt, `go vet`, golangci-lint |
| `ci-gates` | level 3 |
| `ci-engine` | level 1 with `-race`, the `examples/go-compose` module |
| `ci-false-hit` | the false-hit corpus, glob tables, the manifest golden and the append-only check on goldens: a change to the golden tree's manifests fails unless `fingerprint.Version` is bumped |
| `ci-e2e` | levels 2 and 4, run through `aval run -f aval.e2e.yaml` (reused in CI on a verified hit), including the `examples/uv-monorepo` shards (needs uv) |
| `ci-release` | level 5 against a snapshot dry-run of the release build |
| `ci-fuzz` | every `Fuzz*` target for `FUZZTIME` each |
| `ci-security` | govulncheck on both modules (needs network) |
| `ci-docs` | markdownlint and vale (`make docs-lint`), `docs/cli-reference.md` matches the cobra commands, the site builds with `--strict` (`make docs-site`), the "Try it" terminal works in headless Chrome (`make docs-demo-smoke`), the mobile menu opens the site navigation on the home page and an inner page (`make docs-nav-smoke`), and `scripts/deploy-docs.sh` publishes the versions, default and root installers as expected and the outdated banner shows only on releases older than `latest` (`make docs-deploy-test`); on a PR, the built site is uploaded for preview |

CI also dogfoods Aval (#28). `dogfood-fingerprint` and `dogfood-compare`
check that Aval's own contracts (`aval.yaml`, `aval.e2e.yaml`) fingerprint
identically across OSes and checkout paths. `ci-e2e` reuses a verified passing
e2e result for the same inputs and otherwise runs the suite. `dogfood-shadow`
runs the unit suite with `aval run --shadow` and fails on a false hit (exit 1).
Both fail on a verification failure (exit 5), read
`ghcr.io/trianalab/aval-attestations` anonymously and upload their reports
(the `aval verify --output json` format) as artifacts. On `main`,
`dogfood-publish`, the only job with `id-token: write`, signs keyless and
pushes the results that were not reused; it runs no tests. Both suites also
write a run record (`--stats-file`), and on `main` `dogfood-record`
(`packages: write` only, no tests) publishes them with `aval stats publish`
(the pinned release in ci.yaml still uses `aval record`) to the
public stats registry `ghcr.io/trianalab/aval-stats`, whatever the suites'
outcome; a record that cannot be published is a warning. `dogfood-shadow`
adds `aval stats --by repository --since 30d` over that registry to its job
summary (read anonymously, informational), and `docs.yaml` publishes the same
numbers as the site's `stats.json` twice an hour
([Stats](https://aval.run/main/stats/)).
Trust comes from `.github/aval/main.yaml` (main and releases: only `ci.yaml`
on `main`, runner level) and `.github/aval/pr.yaml` (pull requests: also
developer signatures); `tests/architecture/dogfood_test.go` enforces both,
and fails if a dogfood suite reads a tracked file outside its contract's
scope. On a pull request, `.github/aval/trust-from-base.sh` reads `pr.yaml`
and the registry from `main` (never from the PR or its base branch), so a PR
cannot change them by editing those files, and the jobs that pass `--policy`
verify with the pinned release (see below), not with an `aval` or a
`Makefile` from the PR. Those jobs run `main`'s copies of
`trust-from-base.sh` and `install.sh` with `/bin/bash`, and both scripts call
their tools by absolute path; if `main` has no `trust-from-base.sh` yet, no
policy is set and nothing is reused. `tests/architecture/policy_source_test.go`
fails if a pull request job names `AVAL_POLICY` or `AVAL_REGISTRY` anywhere
but the trust step and the `--policy`/`--registry` values, runs anything but
`actions/checkout`, `actions/setup-go` and `sigstore/cosign-installer` before
the trust step, sets `PATH`, `BASH_ENV` or `ENV` there, or passes the policy
through anything but one `aval` command. The jobs that use the policy need
`ci-gates`, so their checks are skipped when the gates fail.

None of this stops a hostile PR from turning its own checks green: on
`pull_request`, GitHub runs the PR's own `ci.yaml`, which can skip the trust
step or edit the tests. What protects `main` is review plus `main` trusting
only its own CI (`main.yaml`): `.github/CODEOWNERS` names owners for
`.github/aval/`, `.github/workflows/`, `tests/architecture/`, `AGENTS.md`,
the `Makefile` and the `aval*.yaml` contracts, and the `main` ruleset must
require code-owner review for that to bind (a maintainer setting).
The release job refuses to publish without a verified, passing e2e
attestation for the tagged commit. Roadmap milestone 3 (an end-to-end example
in GitHub Actions) is in progress: the `example-go-compose` job runs the
publish, reuse and miss story for `examples/go-compose` on every push to
`main`, and the release pipeline for signed binaries (checksums, SBOMs, SLSA
provenance) dry-runs on every PR. CI also runs, on pull
requests, `dependency-review` and `pr-title` (the PR title is a conventional commit, see [Commits](#commits)). `dependency-review` is skipped until a
maintainer enables Settings > Code security > Dependency graph and sets the
repository variable `DEPENDENCY_REVIEW=true`; without the dependency graph the
action fails on every PR. `security.yaml` reruns govulncheck daily, which also
covers merges by the Dependabot workflow (they use `GITHUB_TOKEN` and trigger
no workflow); see `SECURITY.md` for Dependabot and how to report a
vulnerability. A PR that changes only Markdown or `docs/` skips every job
except `ci-gates`, `ci-false-hit`, `ci-docs`, `dependency-review` and `pr-title`. The `required` job aggregates every
other job and is the only required status check; `tests/architecture` fails if
a job is not wired into it. It also enforces that `pkg/evidence` depends only
on the standard library and allowlisted modules, that no Go code names a
consumer, that every action is pinned by commit SHA, that every `TODO` names a
milestone, that the golangci-lint pin matches between `Makefile` and CI, that the
complexity cap stays at 15, and that every relative link and heading anchor in
the Markdown resolves (external URLs are not fetched, so the gate stays
offline).

#### Which aval a CI job runs

A job that tests the PR's fingerprinting builds `aval` from source:
`dogfood-fingerprint`. A job that uses Aval as a user would, or decides
reuse, runs a pinned release instead: the heavy suite (`ci-e2e`, the same
`aval run -f aval.e2e.yaml` as `make ci-e2e`, but not through the PR's
`Makefile` and `bin/aval`, #134), the demo (`example-go-compose`), the shadow
run (`dogfood-shadow`) and the publish steps (`dogfood-publish`,
`dogfood-record`). The suites those jobs run still build and test the PR's
code; only the `aval` that looks up, verifies, records and pushes results is
the release.

`ci.yaml` pins the release as `AVAL_VERSION` and `AVAL_SHA256` (the sha256 of
`aval_<version>_linux_amd64.tar.gz`). `.github/aval/install.sh` checks the
archive against that sha256, so a re-tagged release cannot change what runs,
verifies the keyless signature of `checksums.txt` with cosign for the exact
release workflow identity at that tag, and checks that `checksums.txt` lists
the archive with the same sha256. Any failure fails the job before `aval` is
installed. To move the pin, take the sha256 from the new release's verified
`checksums.txt` (see [Changesets and releases](#changesets-and-releases)) and
update both values; `tests/architecture` checks that the version has a
`CHANGELOG.md` section and that each of these jobs installs the release and
builds nothing.

Markdown syntax rules live in `.markdownlint-cli2.jsonc`, prose rules (no
marketing words, no filler) in `.vale.ini` and `.vale/styles/Aval/`. Both lint
every Markdown file except `.agents/`, `.claude/`, `CLAUDE.md`, `.changeset/` and the generated `CHANGELOG.md`.

### Save PR CI time: sign a local run

A maintainer whose keyless identity is in `.github/aval/pr.yaml` (trust level
`developer`) can run the e2e suite on their laptop, sign the result with their
own Sigstore identity and publish it. When the PR's inputs have the same
fingerprint, `ci-e2e` reuses that result instead of running the suite.

1. Check out the exact commit you push, with no local changes inside the e2e
   contract's scope and `GOFLAGS`, `GOEXPERIMENT` and `CGO_ENABLED` as in CI
   (unset), or the fingerprint differs.
2. Log in to GHCR with a token that can write the
   `aval-attestations` package (`write:packages`):

    ```sh
    echo "$GHCR_TOKEN" | docker login ghcr.io -u <github-user> --password-stdin
    ```

3. Run, sign and push:

    ```sh
    make build
    bin/aval run -f aval.e2e.yaml --registry ghcr.io/trianalab/aval-attestations \
      --policy .github/aval/pr.yaml --push
    ```

    Without `--key` or `$SIGSTORE_ID_TOKEN` and with a terminal, Aval runs
    the suite, then opens the Sigstore login (`oauth2.sigstore.dev`) in the
    browser. Log in with GitHub: the certificate then names your verified
    email with issuer `https://github.com/login/oauth`. The token is used for
    this signature only and not stored. Without a terminal, add `--login`.

PR CI then prints, in `ci-e2e`:

```text
verified aval-e2e sha256:<fingerprint>
signer: <your email> (issuer https://github.com/login/oauth), trust level developer
reference: ghcr.io/trianalab/aval-attestations@sha256:<digest>
reused passed result
```

`main` and releases never reuse this result. `.github/aval/main.yaml` lists
your identity too, but at trust level `developer`, below its `minTrustLevel:
runner`; only `ci.yaml` on `main` signs at runner level, and
`tests/architecture` fails if any other identity gets runner level there. When
a PR merges unchanged, `ci-e2e` on `main` computes the same fingerprint, finds
only your result and prints a miss naming the minimum trust level, then runs
the suite:

```text
miss aval-e2e sha256:<fingerprint>
reference: ghcr.io/trianalab/aval-attestations@sha256:<digest>
reason: not reusable under the trust policy: signer "<your email>" has trust level developer, policy requires at least runner
ran aval-e2e: passed (exit 0)
```

`dogfood-publish` then pushes CI's own runner-signed result next to yours, and
later runs on `main` reuse that one. A release requires a verified runner
result: `release.yaml`'s `aval verify` exits 4 (a miss) on a developer-only
fingerprint and stops. A signer that neither policy lists is still a
verification failure (exit 5). To add a maintainer, add the identity at
`developer` level to both `pr.yaml` and `main.yaml`. PR CI reads `pr.yaml` from
`main`, so the new identity counts only after that change is merged,
never in the PR that adds it.

### Join the public stats

A repository appears on the [Stats](https://aval.run/main/stats/) page once it publishes run records to
`ghcr.io/trianalab/aval-stats`:

1. A TrianaLab maintainer grants the repository write access to the package
   (package settings, "Manage Actions access"). GHCR offers this only to
   repositories of the TrianaLab organization; to ask, open an issue.
2. A job on `main` with `packages: write`, which runs no tests, publishes the
   records the test jobs wrote:

    ```sh
    aval stats publish --source-repo "$GITHUB_REPOSITORY" \
      --registry ghcr.io/trianalab/aval-stats record.json
    ```

    Without `--source-repo` here or on `aval run`, the repository shows as
    `unknown`. Aval's own `dogfood-record` job in `ci.yaml` does this.

A maintainer can refresh the site's `stats.json` at any time from the Actions
tab: open the `docs` workflow and choose "Run workflow" on `main`.

## Commits

[Conventional Commits](https://www.conventionalcommits.org): `<type>[(scope)][!]: <description>`, with type one of `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `ci`, `perf`, `build`, `style`. Scope is optional, e.g. `feat(fingerprint): ...`; `!` marks a breaking change.

PRs are squash-merged with the PR title as the commit message, so the `pr-title` CI job rejects a PR whose title is not in that format. Editing the title reruns CI.

## Docs site

`mkdocs.yml` builds `docs/` into a MkDocs Material site. Every page in `docs/`
needs an entry in its `nav`; `make docs-site` fails otherwise.
`.github/workflows/docs.yaml` publishes it to GitHub Pages with mike: a push to
`main` deploys version `main`, a release tag `v<version>` deploys `<version>`
with the `latest` alias.

### Build and preview

```sh
pip install --require-hashes -r docs/requirements.txt
mkdocs serve      # live preview on http://127.0.0.1:8000
make ci-docs      # the CI docs gates
make test-architecture  # links, doc examples, page length, size budget
```

On a pull request, `ci-docs` uploads the built site as the `docs-site`
artifact (kept 7 days). Download it from the run page, unzip it and run
`python3 -m http.server -d <dir>` to read it as the reviewer sees it.

### Recordings

The GIFs in `docs/assets/gifs/` are recorded from `docs/assets/tapes/*.tape`.
After a change to the CLI output or a tape, run `make docs-demos` (needs vhs,
Docker and openssl) and commit the GIFs.

### Rules

`tests/architecture` enforces these:

- A concept page (`docs/concepts/`) stays under 80 lines and a task guide
  (`docs/guides/`) under 120; `README.md` stays under 150. Put details in collapsible
  blocks or link to the reference.
- A guide opens with a `Goal:` sentence, has a diagram and ends with
  `## Read next`. Its YAML, Go and JSON come from tested files through
  snippets, so they cannot drift.
- Every contract and policy example in the docs parses with Aval's own
  parsers. Exit codes are listed in one table only, in
  `docs/cli-reference.md`; other pages include it.
- Every relative link and heading anchor resolves.
- Size budget: each image under 100 KiB, each recording under 1.5 MiB, and all
  images and recordings under `docs/` and `overrides/` under 2 MiB together.

Prose follows the Vale rules (short, concrete, no marketing words) and
markdownlint. Docs describe only what the code does; mark future work with
its milestone.

One-time human step: enable Pages with source "GitHub Actions" (Settings >
Pages), and let release tags `v*` deploy to the `github-pages` environment
(Settings > Environments > github-pages > Deployment branches and tags). Until
Pages is enabled, the workflow skips the deploy with a notice.

## Changesets and releases

A PR that changes what users get (the CLI, `pkg/evidence`, formats) adds a
changeset: `npx changeset` (after `npm ci`) writes `.changeset/<name>.md` with
the bump (`patch`, `minor`, `major`) and one line for the changelog. Pre-1.0, a
breaking change is a `minor`.

On every push to `main`, `.github/workflows/release.yaml` keeps a `chore:
version packages` PR open that applies the pending changesets: it sets the
version in `package.json` and writes `CHANGELOG.md`. `package.json` only holds
the version; Aval is not an npm package. To release:

1. Close and reopen the version PR so `ci` runs on it (PRs opened with
   `GITHUB_TOKEN` trigger no workflows), then merge it.
2. Wait for the release. When `ci` on `main` passes for the merge commit (its
   `dogfood-publish` job pushes the e2e result to
   `ghcr.io/trianalab/aval-attestations`), the `auto-tag` job in
   `release.yaml` tags it `v<version from package.json>` and dispatches the
   `release` job on that tag; once the GitHub Release exists, the `release`
   job dispatches the docs deploy. `auto-tag` tags the first green commit with
   no pending changeset, so the tag can point at a commit after the merge. It
   tags nothing when the tag exists, the version is older than the newest
   `v*` tag, the commit is no longer on `main`, or a `.changeset/*.md` entry
   is pending. A feature merge does not change the version, so its tag
   already exists and nothing is released. The `release` job refuses to
   publish without a verified, passing e2e attestation for that commit
   (`aval verify -f aval.e2e.yaml --registry
   ghcr.io/trianalab/aval-attestations --policy .github/aval/main.yaml`).
   A maintainer can still tag by hand: push the tag, or re-run a failed
   release with Actions > release > Run workflow on the tag (and Actions >
   docs > Run workflow on the tag if the docs deploy did not run). If a
   failed release left a partial GitHub Release, delete it first with `gh
   release delete <tag> --yes`, which keeps the tag. If no release appears
   because `ci` for the merge commit failed or was cancelled, re-run that
   `ci` run (`auto-tag` fires when it passes) or tag the merge commit by hand.

One-time human step, before the first release after v0.1.0: when the first
`main` run creates the `aval-attestations` package, make it public in its
package settings. The `release`, `ci-e2e` and `dogfood-shadow` jobs read it
anonymously. While it is private every lookup is an error: the suites run
every time and every release is blocked.

The tag runs the `release` job: GoReleaser (`.goreleaser.yaml`) builds the
binaries for Linux, macOS and Windows (amd64, arm64), archives, `checksums.txt`
and one SPDX SBOM per archive (syft), and signs `checksums.txt` keyless with
cosign; `actions/attest-build-provenance` adds SLSA provenance for every file
in `checksums.txt`. `tests/release` then verifies all of it, including that
the tag equals the `package.json` version, and only then the job creates the
GitHub Release. It uploads only the files `checksums.txt` lists, plus
`checksums.txt` and its signature and provenance bundles. If any check fails,
nothing is published. `make test-release` runs the same build as an unsigned
snapshot and the same tests; CI runs it on every PR that changes code
(`ci-release`).

To verify a downloaded release (`<tag>` is e.g. `v0.1.0`):

```sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity https://github.com/TrianaLab/aval/.github/workflows/release.yaml@refs/tags/<tag>
sha256sum --ignore-missing -c checksums.txt
gh attestation verify aval_<version>_linux_amd64.tar.gz --repo TrianaLab/aval
```

CI runs the first two checks, plus a pinned archive sha256, before it uses a
release (see [Which aval a CI job runs](#which-aval-a-ci-job-runs)).

## Pull requests and issues

Opening a PR fills in `.github/pull_request_template.md`; keep its checklist. Bugs and feature requests use the issue forms in `.github/ISSUE_TEMPLATE/`. Report security issues privately as described in [SECURITY.md](SECURITY.md), not in a public issue.

## Code of conduct

Participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).

## Design rules PRs must not break

- **Fail closed.** If input equivalence cannot be proven (missing dependency file, symlink, empty scope), there is no reuse and the test runs. Ambiguous contracts (unknown fields, bad globs) are rejected. Never guess.
- **Scope is declared in the contract.** `aval.yaml` is the only source of inputs. No inference or runtime tracing in v0.
- **Miss is not verification failure.** No attestation is exit 4 (`ErrNotFound`); an attestation that fails signature or policy is exit 5 (`ErrVerification`), a security event. Keep them separate in code, output and exit codes, and never hide the second.
- **No Pacto dependency.** Aval does not import Pacto or model its concepts. Integrations consume Aval through `pkg/evidence` and `aval verify --output json`; adapters live in the consumer's repo.
- **`pkg/evidence` is public API.** Breaking changes (rename, removal, type or meaning change) to exported identifiers, the predicate or the verify report JSON need a version bump (`PredicateType`, `ReportVersion`) and a note in `docs/artifact-format.md`; additive changes do not. The rules are in `docs/artifact-format.md` ("Versioning and compatibility"); golden files in `pkg/evidence/testdata` guard the JSON shape.
- **Fingerprint changes bump the manifest version** (`aval-fingerprint/v1`) so old and new fingerprints never collide. `TestManifestGolden` (`ci-false-hit`) enforces it: it pins, per version, the manifests of three contracts over `internal/fingerprint/testdata/golden/tree` (one sets every field, one omits every optional field, one sets them empty) and fails on any byte change to them under the same version. The golden tree and the corpus together catch every manifest-changing mutant of the manifest code (the mutation sweep is described under [False-hit suite](#false-hit-suite)); a path that tree does not exercise is covered only by the corpus, so a new manifest feature needs a case in the golden tree (see [False-hit suite](#false-hit-suite)). `TestGoldensAppendOnly` stops the golden being rewritten to match: a committed `*.manifest` golden may be added but never modified, renamed or deleted, checked against the merge base with `AVAL_GOLDEN_BASE` (CI: the PR base branch or the previous push; the job fails if it does not resolve, and after a force-push the old `before` may be unreachable, so the job fails until a normal push) or, under `make ci-false-hit` without it, `origin/main` (fails if that ref is missing; a plain `go test` without `AVAL_GOLDEN_BASE` skips this one test). After a bump, `go test ./internal/fingerprint -run TestManifestGolden -update` writes the golden of the new version.

## Unfinished work

Deferred work carries a `TODO(milestone-N): ...` comment naming the roadmap milestone that lands it (see [docs/vision.md](docs/vision.md)); no bare `TODO`. Nothing returns `evidence.ErrNotImplemented` (deprecated) and exit 6 stays reserved, never returned. Deferred code still counts toward the 100% coverage gate (`make coverage-check`).

## License

Contributions are accepted under the Apache-2.0 license (see [LICENSE](LICENSE)). No CLA or DCO sign-off required.

## Working with coding agents

`AGENTS.md` holds the project rules for humans and agents alike. Portable skills for implementing the roadmap live in `.agents/skills/` (symlinked into `.claude/skills/`); see the table at the end of `AGENTS.md`.
