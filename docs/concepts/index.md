# How Aval works

Aval runs a test once, signs the result and stores it in your OCI registry.
The next job with the same inputs checks that signature and skips the test.

```mermaid
flowchart LR
  run([aval run]) --> fp[fingerprint] --> look[look up] --> verify{trusted?}
  verify -- yes --> reuse([reuse]):::ok
  verify -- no --> exec[run the test] --> sign[sign] --> push([push]):::muted
```

## The steps

1. **Fingerprint.** Aval reads `aval.yaml`, the test's contract. It hashes
   the contract and every file it lists into one fingerprint.
2. **Look up.** Aval asks your registry for results stored under that
   fingerprint.
3. **Verify.** Each result must carry a valid signature from a signer your
   policy trusts. See [Trust](trust.md).
4. **Reuse or run.** A verified result is reused and the test does not run.
   Anything else runs the test. See [Outcomes](outcomes.md).
5. **Sign and push.** With `--push`, Aval signs the new result and pushes it,
   so the next job can reuse it.

If Aval cannot prove that reuse is safe, the test runs.

## An example

A contract for a Go integration test:

```yaml
apiVersion: aval.trianalab.io/v1
kind: Test
metadata:
  name: integration
runner:
  command: ["go", "test", "-tags=integration", "./orders/..."]
scope:
  source: ["orders/**/*.go", "testdata/orders/**"]
  dependencies: ["go.mod", "go.sum"]
cacheability: hermetic
```

The first job finds nothing, runs the test and pushes a signed result:

```console
$ aval run --push --key keys/ci.pem --registry $REG --policy policy.yaml
miss integration sha256:b40e9fac…
ran integration: passed (exit 0)
pushed localhost:5055/acme/orders-aval@sha256:2496759a…
```

A second job with the same inputs reuses it:

```console
$ aval run --registry $REG --policy policy.yaml
verified integration sha256:b40e9fac…
reused passed result …/orders-aval@sha256:2496759a…
```

Change one file in scope and the fingerprint changes: the test runs again.

## What Aval does not do

- It does not guess inputs. A file the contract does not list is invisible
  to it.
- It runs no server. Results live in an OCI registry you already have.

!!! info "The full specs"
    These pages keep things short. The details are in the specs:
    [the test contract](../contract.md), [the trust model](../trust-model.md)
    and [the artifact format](../artifact-format.md).
