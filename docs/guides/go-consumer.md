# Consume from Go

Goal: check an Aval result from your own Go program with `pkg/evidence`, and
tell a miss from a security event.

```mermaid
flowchart LR
  fetch([envelope bytes,<br>fetched by digest]) --> check[Check]
  check -- nil bytes --> miss([ErrNotFound:<br>run the test]):::muted
  check -- ErrVerification --> alert([alert,<br>do not reuse]):::err
  check -- ok --> p([predicate]):::ok
```

## The example

Add the module:

```sh
go get github.com/TrianaLab/aval@latest
```

This is `Check` from
[`examples/evidence-consumer`](https://github.com/TrianaLab/aval/blob/main/examples/evidence-consumer/consumer.go),
a separate module that imports only `pkg/evidence`. CI builds and tests it:

```go
--8<-- "examples/evidence-consumer/consumer.go:check"
```

Branch on what it returns, with `errors.Is`:

| `Check` returns | Meaning | Do |
|---|---|---|
| a predicate with `Result == evidence.ResultPassed` | a trusted pass for these inputs | reuse it |
| `evidence.ErrNotFound` | nothing to reuse: a miss | run the test |
| `evidence.ErrVerification` | a result that does not verify | alert, run the test, do not reuse |
| a predicate with another `Result` | a trusted failure for these inputs | treat the test as failed |

## What you bring

- **The envelope bytes.** Fetch them by digest with ORAS or any registry
  client. The layout is in [Artifact format](../artifact-format.md).
- **A `SignatureVerifier`.** `pkg/evidence` checks the policy, not the
  cryptography. The example brings an ed25519 one; a Sigstore one for
  consumers is planned for milestone 5.
- **The identities.** Build them from your own policy. Trust never comes from
  the predicate.

??? info "Read an `aval verify` report instead"
    If you run `aval verify --output json`, read its report with
    `evidence.Report`. The example's `Reuse` function does this and fails
    closed on any `verification-failed` candidate.

## Read next

- [Consume evidence](../integrations.md): every symbol of `pkg/evidence`
- [Go API compatibility](../integrations.md#go-api-compatibility): additive within a major version
- [Outcomes](../concepts/outcomes.md)
