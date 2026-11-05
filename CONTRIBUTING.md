# Contributing

## Setup

- Go version: whatever `go.mod` says (currently 1.27).
- [golangci-lint](https://golangci-lint.run) at the exact version `GOLANGCI_LINT_VERSION` in the `Makefile` (CI installs the same one; `make lint` refuses any other). Config in `.golangci.yml`, including the cyclomatic complexity cap of 15.
- For `make test-release`: [syft](https://github.com/anchore/syft) on `PATH` (CI pins the version). GoReleaser is installed into `bin/` at `GORELEASER_VERSION`.
- For `make docs-lint`: Node.js (`npx` fetches markdownlint-cli2 at the pinned `MARKDOWNLINT_CLI2_VERSION`) and [Vale](https://vale.sh) at the exact `VALE_VERSION` in the `Makefile`.

```sh
make build   # bin/aval
make ci      # every gate CI runs; run it before every push
make lint    # go vet + golangci-lint
make fmt     # gofmt -w .
make docs-lint  # markdownlint + vale over every Markdown file
make docs-cli   # regenerate docs/cli-reference.md after changing a command or flag
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
| `ci-e2e` | levels 2 and 4, run through `aval run -f aval.e2e.yaml` (reused in CI on a verified hit) |
| `ci-release` | level 5 against a snapshot dry-run of the release build |
| `ci-fuzz` | every `Fuzz*` target for `FUZZTIME` each |
| `ci-security` | govulncheck on both modules (needs network) |
| `ci-docs` | markdownlint and vale (`make docs-lint`), and `docs/cli-reference.md` matches the cobra commands |

CI also dogfoods Aval (#28). `dogfood-fingerprint` and `dogfood-compare`
check that Aval's own contracts (`aval.yaml`, `aval.e2e.yaml`) fingerprint
identically across OSes and checkout paths. `ci-e2e` reuses a verified passing
e2e result for the same inputs and otherwise runs the suite. `dogfood-shadow`
runs the unit suite with `aval run --shadow` and fails on a false hit (exit 1).
Both fail on a verification failure (exit 5), read
`ghcr.io/trianalab/aval-attestations` anonymously and upload their reports
(the `aval verify --output json` format) as artifacts. On `main`,
`dogfood-publish`, the only job with `packages: write` and `id-token: write`,
signs keyless and pushes the results that were not reused; it runs no tests.
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
except `ci-gates`, `ci-docs`, `dependency-review` and `pr-title`. The `required` job aggregates every
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
   commit (`aval verify -f aval.e2e.yaml --policy .github/aval/main.yaml`).
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
- **Fingerprint changes bump the manifest version** (`aval-fingerprint/v1`) so old and new fingerprints never collide.

## Unfinished work

Stubs return `evidence.ErrNotImplemented` (exit 6) and carry a `TODO(milestone-N): ...` comment naming the roadmap milestone that removes them (see [docs/vision.md](docs/vision.md)). Use the same convention; no bare `TODO`. Stubs still count toward the 100% coverage gate (`make coverage-check`): cover each with a test that asserts it returns `ErrNotImplemented`.

## License

Contributions are accepted under the Apache-2.0 license (see [LICENSE](LICENSE)). No CLA or DCO sign-off required.

## Working with coding agents

`AGENTS.md` holds the project rules for humans and agents alike. Portable skills for implementing the roadmap live in `.agents/skills/` (symlinked into `.claude/skills/`); see the table at the end of `AGENTS.md`.
