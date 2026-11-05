# Contributing

## Setup

- Go version: whatever `go.mod` says (currently 1.25).
- [golangci-lint](https://golangci-lint.run) at the exact version `GOLANGCI_LINT_VERSION` in the `Makefile` (CI installs the same one; `make lint` refuses any other). Config in `.golangci.yml`, including the cyclomatic complexity cap of 15.
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
| 5 Release | the release produces what it claims (from milestone 3) | `tests/release/` | `make test-release` |

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
| `ci-integration` | level 2 |
| `ci-acceptance` | level 4 |
| `ci-release` | level 5 |
| `ci-fuzz` | every `Fuzz*` target for `FUZZTIME` each |
| `ci-security` | govulncheck on both modules (needs network) |
| `ci-docs` | markdownlint and vale (`make docs-lint`), and `docs/cli-reference.md` matches the cobra commands |

CI also runs `dogfood-fingerprint` and `dogfood-compare` (Aval's own contract
must fingerprint identically across OSes and checkout paths) and, on pull
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

Markdown syntax rules live in `.markdownlint-cli2.jsonc`, prose rules (no
marketing words, no filler) in `.vale.ini` and `.vale/styles/Aval/`. Both lint
every Markdown file except `.agents/`, `.claude/` and `CLAUDE.md`.

## Commits

[Conventional Commits](https://www.conventionalcommits.org): `<type>[(scope)][!]: <description>`, with type one of `feat`, `fix`, `docs`, `chore`, `refactor`, `test`, `ci`, `perf`, `build`, `style`. Scope is optional, e.g. `feat(fingerprint): ...`; `!` marks a breaking change.

PRs are squash-merged with the PR title as the commit message, so the `pr-title` CI job rejects a PR whose title is not in that format. Editing the title reruns CI.

## Pull requests and issues

Opening a PR fills in `.github/pull_request_template.md`; keep its checklist. Bugs and feature requests use the issue forms in `.github/ISSUE_TEMPLATE/`. Report security issues privately as described in [SECURITY.md](SECURITY.md), not in a public issue.

## Code of conduct

Participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md).

## Design rules PRs must not break

- **Fail closed.** If input equivalence cannot be proven (missing dependency file, symlink, empty scope), there is no reuse and the test runs. Ambiguous contracts (unknown fields, bad globs) are rejected. Never guess.
- **Scope is declared in the contract.** `aval.yaml` is the only source of inputs. No inference or runtime tracing in v0.
- **Miss is not verification failure.** No attestation is exit 4 (`ErrNotFound`); an attestation that fails signature or policy is exit 5 (`ErrVerification`), a security event. Keep them separate in code, output and exit codes, and never hide the second.
- **No Pacto dependency.** Aval does not import Pacto or model its concepts. Integrations consume Aval through `pkg/evidence` and `aval verify --output json`; adapters live in the consumer's repo.
- **`pkg/evidence` is public API.** Breaking changes (rename, removal, type or meaning change) to exported identifiers, the predicate or the verify report JSON need a version bump (`PredicateType`, `ReportVersion`) and a note in `docs/artifact-format.md`; additive changes do not. Formats are provisional until v1, but changes are still deliberate.
- **Fingerprint changes bump the manifest version** (`aval-fingerprint/v1`) so old and new fingerprints never collide.

## Unfinished work

Stubs return `evidence.ErrNotImplemented` (exit 6) and carry a `TODO(milestone-N): ...` comment naming the roadmap milestone that removes them (see [docs/vision.md](docs/vision.md)). Use the same convention; no bare `TODO`. Stubs still count toward the 100% coverage gate (`make coverage-check`): cover each with a test that asserts it returns `ErrNotImplemented`.

## License

Contributions are accepted under the Apache-2.0 license (see [LICENSE](LICENSE)). No CLA or DCO sign-off required.

## Working with coding agents

`AGENTS.md` holds the project rules for humans and agents alike. Portable skills for implementing the roadmap live in `.agents/skills/` (symlinked into `.claude/skills/`); see the table at the end of `AGENTS.md`.
