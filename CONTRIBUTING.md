# Contributing

## Setup

- Go version: whatever `go.mod` says (currently 1.25).
- [golangci-lint](https://golangci-lint.run) v2 (CI pins v2.6; config in `.golangci.yml`).

```sh
make build   # bin/aval
make ci      # every gate CI runs; run it before every push
make lint    # go vet + golangci-lint
make fmt     # gofmt -w .
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

CI also runs `dogfood-fingerprint` and `dogfood-compare` (Aval's own contract
must fingerprint identically across OSes and checkout paths). A PR that changes
only Markdown or `docs/` skips every job except `ci-gates`. The `required` job
aggregates every other job and is the only required status check;
`tests/architecture` fails if a job is not wired into it.

## Commits

[Conventional Commits](https://www.conventionalcommits.org): `feat:`, `fix:`, `docs:`, `refactor:`, `test:`, `chore:`. Scope is optional, e.g. `feat(fingerprint): ...`.

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
