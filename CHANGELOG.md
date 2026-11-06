# aval

## 0.2.0

### Minor Changes

- 0148c76: Formats are frozen. The contract and policy `apiVersion` is now `aval.trianalab.io/v1` and the verify report `version` is `aval.trianalab.io/verify-report/v1`; `aval.dev/v1alpha1` contracts and policy files are rejected, so change that line in yours. The contract hash covers `apiVersion`, so results published before are misses and each test runs once. The predicate type (`https://aval.trianalab.io/attestation/execution/v2`), media and artifact types (`application/vnd.trianalab.aval.execution.v2`, `application/vnd.trianalab.aval.fingerprint.v1`) and predicate `schemaVersion` `v2` are unchanged and now frozen, so attestations written by v0.1.0 keep a valid, readable format. Compatibility rules (additive changes within a version, what needs a new version, how consumers treat unknown fields and values, deprecation) are in `docs/artifact-format.md`, and golden files guard the JSON shape of the statement, predicate and verify report.

### Patch Changes

- c17dc26: `aval run` and `aval verify` escape newlines and control characters in reasons printed as text and on stderr, so text from an untrusted attestation cannot start a log line of its own. Aval's own CI now reuses its signed e2e results, and a release requires a verified e2e attestation for its commit.
- 59b9b66: `pkg/evidence` is semver-stable Go API: within a major version it changes additively only, and CI fails when an exported identifier is removed or changes type or signature without a new major module path. `examples/evidence-consumer` is a separate module that consumes Aval evidence through `pkg/evidence` alone, with its own signature verifier. The policy is in `docs/integrations.md`.
- bbf9e55: Fingerprinting fails closed in two more cases: a `scope.source` glob that matches no file (a typo, or a name that differs on disk by case or Unicode form), and a file excluded only because a trailing `**` in `exclude` matched zero segments (`build/**` against a file named `build`). Trees that fingerprinted before and still do keep the same fingerprint. A table-driven false-hit corpus now pins every kind of relevant and irrelevant change.

## 0.1.0

### Minor Changes

- a6d2b1a: First release: signed `aval` binaries for Linux, macOS and Windows with checksums, SBOMs and SLSA provenance. `aval --version` prints the release version.

### Patch Changes

- 59664a3: Dependency updates: `oras.land/oras-go/v2` 2.6.4 to 2.7.0; GitHub Actions `actions/checkout` to v7.0.1, `actions/setup-go` to v7.0.0, `actions/upload-artifact` to v7.0.2, `actions/download-artifact` to v8.0.2 and `actions/dependency-review-action` to v5.0.0 (Node 24 runtime).
