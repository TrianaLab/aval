# Contributing

## Setup

- Go version: whatever `go.mod` says (currently 1.27).
- [golangci-lint](https://golangci-lint.run) at the exact version `GOLANGCI_LINT_VERSION` in the `Makefile` (CI installs the same one; `make lint` refuses any other). Config in `.golangci.yml`, including the cyclomatic complexity cap of 15.
- For `make test-release`: [syft](https://github.com/anchore/syft) on `PATH` (CI pins the version). GoReleaser is installed into `bin/` at `GORELEASER_VERSION`.
- For `make docs-lint`: Node.js (`npx` fetches markdownlint-cli2 at the pinned `MARKDOWNLINT_CLI2_VERSION`) and [Vale](https://vale.sh) at the exact `VALE_VERSION` in the `Makefile`.
- For `make docs-site`: Python 3.12 and the hash-pinned tools, `pip install --require-hashes -r docs/requirements.txt` (regenerate that file from `docs/requirements.in`, command in its header).

```sh
make build   # bin/aval
make ci      # every gate CI runs; run it before every push
make lint    # go vet + golangci-lint
make fmt     # gofmt -w .
make docs-lint  # markdownlint + vale over every Markdown file
make docs-cli   # regenerate docs/cli-reference.md after changing a command or flag
make docs-site  # strict build of the MkDocs site into site/; preview with `mkdocs serve`
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

### CI quality gates

`make ci` runs these legs; each is one job of the same name in
`.github/workflows/ci.yaml`:

| Leg | What it checks |
|---|---|
| `ci-static` | gofmt, `go vet`, golangci-lint |
| `ci-gates` | level 3 |
| `ci-engine` | level 1 with `-race`, the `examples/go-compose` module |
| `ci-false-hit` | the false-hit corpus, glob tables, the manifest golden and the append-only check on goldens: a change to the golden tree's manifests fails unless `fingerprint.Version` is bumped |
| `ci-e2e` | levels 2 and 4, run through `aval run -f aval.e2e.yaml` (reused in CI on a verified hit) |
| `ci-release` | level 5 against a snapshot dry-run of the release build |
| `ci-fuzz` | every `Fuzz*` target for `FUZZTIME` each |
| `ci-security` | govulncheck on both modules (needs network) |
| `ci-docs` | markdownlint and vale (`make docs-lint`), `docs/cli-reference.md` matches the cobra commands, the site builds with `--strict` (`make docs-site`), the mobile menu opens the site navigation on the home page and an inner page (`make docs-nav-smoke`), and `scripts/deploy-docs.sh` publishes the versions, default and root installers as expected (`make docs-deploy-test`); on a PR, the built site is uploaded for preview |

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
write a run record (`--record-file`), and on `main` `dogfood-record`
(`packages: write` only, no tests) publishes them with `aval record` to the
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
scope.
The release job refuses to publish without a verified, passing e2e
attestation for the tagged commit. CI also runs, on pull
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

A job that tests the PR's code builds `aval` from source: `ci-e2e` (`make
ci-e2e`, the same command as locally) and `dogfood-fingerprint`, which checks
the PR's fingerprinting. A job that uses Aval as a user would runs a pinned
release instead: the demo (`example-go-compose`), the shadow run
(`dogfood-shadow`) and the publish step (`dogfood-publish`). The suites those
jobs run still build and test the PR's code; only the `aval` that looks up,
verifies, records and pushes results is the release. `ci-e2e` could use the
release for the same reason, but it would need a `Makefile` change, and the
`Makefile` is in the e2e contract's scope.

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

Transition: v0.1.0 reads only the pre-freeze `apiVersion` `aval.dev/v1alpha1`,
not the frozen `aval.trianalab.io/v1` of this repository's contracts and
policies. While `AVAL_VERSION` is `0.1.0`, these three jobs build `aval` from
the commit (`make build`) and install nothing; `tests/architecture` enforces
that too. On `main` that is the code being released, so `dogfood-publish`
writes the e2e attestation in the format the `release.yaml` gate (also a
source build) verifies. Moving the pin to the first release that reads
`aval.trianalab.io/v1` makes the test require `install.sh` again. The
transition may outlive one release only: once `CHANGELOG.md` lists two
releases newer than `0.1.0`, `tests/architecture` fails until the pin moves.

Markdown syntax rules live in `.markdownlint-cli2.jsonc`, prose rules (no
marketing words, no filler) in `.vale.ini` and `.vale/styles/Aval/`. Both lint
every Markdown file except `.agents/`, `.claude/`, `CLAUDE.md`, `.changeset/` and the generated `CHANGELOG.md`.

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

- A concept page (`docs/concepts/`) and a task guide (`docs/guides/`) stay
  under 120 lines; `README.md` stays under 150. Put details in collapsible
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
2. Wait until `ci` on `main` has passed for the merge commit (its
   `dogfood-publish` job pushes the e2e result to
   `ghcr.io/trianalab/aval-attestations`), then tag the merge commit
   `v<version from package.json>` and push the tag. The `release` job
   refuses to publish without a verified, passing e2e attestation for that
   commit (`aval verify -f aval.e2e.yaml --registry
   ghcr.io/trianalab/aval-attestations --policy .github/aval/main.yaml`).
   A tag pushed too early fails; re-run the `release` job once `ci` on
   `main` has finished.

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
- **Fingerprint changes bump the manifest version** (`aval-fingerprint/v1`) so old and new fingerprints never collide. `TestManifestGolden` (`ci-false-hit`) enforces it: it pins, per version, the manifests of three contracts over `internal/fingerprint/testdata/golden/tree` (one sets every field, one omits every optional field, one sets them empty) and fails on any byte change to them under the same version. The golden tree and the corpus together catch every manifest-changing mutant of the manifest code (the mutation sweep is described in docs/contract.md); a path that tree does not exercise is covered only by the corpus, so a new manifest feature needs a case in the golden tree (see [docs/contract.md](docs/contract.md#false-hit-corpus)). `TestGoldensAppendOnly` stops the golden being rewritten to match: a committed `*.manifest` golden may be added but never modified, renamed or deleted, checked against the merge base with `AVAL_GOLDEN_BASE` (CI: the PR base branch or the previous push; the job fails if it does not resolve, and after a force-push the old `before` may be unreachable, so the job fails until a normal push) or, under `make ci-false-hit` without it, `origin/main` (fails if that ref is missing; a plain `go test` without `AVAL_GOLDEN_BASE` skips this one test). After a bump, `go test ./internal/fingerprint -run TestManifestGolden -update` writes the golden of the new version.

## Unfinished work

Deferred work carries a `TODO(milestone-N): ...` comment naming the roadmap milestone that lands it (see [docs/vision.md](docs/vision.md)); no bare `TODO`. Nothing returns `evidence.ErrNotImplemented` (deprecated) and exit 6 stays reserved, never returned. Deferred code still counts toward the 100% coverage gate (`make coverage-check`).

## License

Contributions are accepted under the Apache-2.0 license (see [LICENSE](LICENSE)). No CLA or DCO sign-off required.

## Working with coding agents

`AGENTS.md` holds the project rules for humans and agents alike. Portable skills for implementing the roadmap live in `.agents/skills/` (symlinked into `.claude/skills/`); see the table at the end of `AGENTS.md`.
