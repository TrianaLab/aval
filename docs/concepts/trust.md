# Trust

A signed result is reused only if **your** policy trusts the signer. The
result cannot vouch for itself.

```mermaid
flowchart LR
  ci[CI job] --> r[signed result]
  dev[developer key] --> r
  r --> sig{valid<br>signature?}
  sig -- no --> fail([verification-failed<br>exit 5]):::err
  sig -- yes --> who{signer in<br>the policy?}
  who -- no --> fail
  who -- yes --> lvl{level high<br>enough?}
  lvl -- no --> miss([miss<br>exit 4]):::muted
  lvl -- yes --> ok([verified<br>reuse]):::ok
```

All exit codes, from the [CLI reference](../cli-reference.md#exit-codes):

--8<-- "docs/cli-reference.md:exit-codes"

## Signers

A **signer** is who signed a result. There are two kinds:

- **A CI job (keyless).** The job signs with a short-lived certificate from
  Sigstore, tied to its OIDC identity, such as one GitHub Actions workflow
  on `main`.
- **A key.** A developer signs with a local key file.

## Trust levels

Your policy gives each signer a trust level:

--8<-- "docs/trust-model.md:trust-levels"

The predicate inside a result also claims a level. Aval ignores that claim:
the level always comes from your policy.

## The policy

A policy is a YAML file you keep in your repository. This one trusts the
`main` branch workflow for CI reuse, and Alice's key at developer level:

--8<-- "docs/trust-model.md:policy-file"

With `minTrustLevel: runner`, a result Alice signed is a miss: the test runs.
Without `--policy`, nothing is trusted and every test runs.

## What a signature proves

- **Proves:** who signed the result, and that nobody changed it since.
- **Does not prove:** that the test ran honestly, or that inputs outside the
  contract were the same.

So only grant a level to signers you would trust to run the test yourself.

!!! info "The full spec"
    [The trust model](../trust-model.md) covers keyless verification, the
    policy fields and the threats Aval defends against.
