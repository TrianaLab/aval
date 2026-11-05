# Contributing

## Setup

- Go version: whatever `go.mod` says (currently 1.25).
- [golangci-lint](https://golangci-lint.run) v2 (CI pins v2.6; config in `.golangci.yml`).

```sh
make build   # bin/aval
make test    # go test -race ./...
make lint    # go vet + golangci-lint
make fmt     # gofmt -w .
```

CI runs `go vet`, `go test -race` and golangci-lint on every PR.

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

Stubs return `evidence.ErrNotImplemented` (exit 6) and carry a `TODO(milestone-N): ...` comment naming the roadmap milestone that removes them (see [docs/vision.md](docs/vision.md)). Use the same convention; no bare `TODO`.

## License

Contributions are accepted under the Apache-2.0 license (see [LICENSE](LICENSE)). No CLA or DCO sign-off required.
