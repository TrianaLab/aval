# Aval

Test results as signed, verifiable OCI artifacts.

A heavy test runs once. Aval signs the result and binds it to a fingerprint of the test's declared inputs. Another pipeline with the same fingerprint verifies the signature against its trust policy and skips the run.

```mermaid
flowchart LR
  A[Fingerprint inputs] --> B{Result for this fingerprint?}
  B -- no --> R[Run the test]
  B -- yes --> C{Signature and policy verify?}
  C -- yes --> S[Reuse the result]
  C -- no --> F[Run the test, report a security event]
```

!!! note "Fail closed"
    If Aval cannot prove the inputs are the same, or cannot verify who signed the result, the test runs.

## Where to start

- [Why Aval](vision.md): the problem, the goals and the roadmap.
- [Trust model](trust-model.md): what a signature proves and how policy decides.
- [The test contract](contract.md): the `aval.yaml` file and what goes into the fingerprint.
- [CLI reference](cli-reference.md): every command and flag.
