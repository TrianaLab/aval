# Aval

Test results as signed, verifiable OCI artifacts.

Aval is not a test cache. It is a CI primitive for **verifiable test execution reuse**: a heavy test runs once, the result is signed and bound to a fingerprint of its declared inputs, and any other environment with the same fingerprint can verify it and skip the run. A signature proves *who* claims the test passed, not that it ran honestly, so reuse requires an explicit input contract plus a trust policy over signers.

**Problem.** Integration and end-to-end suites run again on every push, in every pipeline, even when nothing they depend on changed. Build caches cover compilation and unit tests well, but rarely heavy tests with services, and they give you no signed record of who ran what.

## How it works

```text
dev / CI:   run test -> fingerprint inputs -> sign (in-toto/DSSE) -> push to OCI registry
                                                                     tag sha256-<fp>
CI on push: fingerprint inputs -> lookup sha256-<fp> -> verify signature + policy -> reuse
                                         | miss                    | fails
                                         v                         v
                                      run test           run test, report security event
```

No server or database: ORAS, Cosign and OCI 1.1 referrers do the work, and artifacts are always consumed by digest. If input equivalence cannot be proven, the test runs.

## Quickstart (target UX, not implemented yet)

```yaml
# aval.yaml
apiVersion: aval.dev/v1alpha1   # provisional
kind: Test
metadata:
  name: orders-integration
runner:
  command: ["go", "test", "-tags=integration", "./..."]
scope:
  source: ["**/*.go", "testdata/**"]
  dependencies: ["go.sum"]
environment:
  toolchain: {go: "1.25"}
  env: [DATABASE_IMAGE]
cacheability: controlled-environment
```

```sh
aval run --push --registry ghcr.io/acme/orders-aval      # reuse a verified result, or run and publish
aval verify --registry ghcr.io/acme/orders-aval --policy policy.yaml   # exit 0 verified, 4 miss, 5 verification failed
```

## Status: pre-alpha

Contract, fingerprint, signing (keyless and key-based), push to and lookup in an OCI registry, verification against a trust policy and `aval run` reuse or execute work. Formats are provisional until milestone 5. `aval run --shadow` verifies and still runs the test, and fails on a false hit.

## Docs

[Vision](docs/vision.md) · [Trust model](docs/trust-model.md) · [Artifact format](docs/artifact-format.md) · [Contract](docs/contract.md) · [Integrations](docs/integrations.md) · [CLI reference](docs/cli-reference.md) · [Example: Go + Compose](examples/go-compose)

## License

Apache-2.0. See [LICENSE](LICENSE).
