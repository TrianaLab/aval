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

What each outcome means: [Outcomes](outcomes.md).

## Signers and levels

| Signer | Signs with | Usual level |
|---|---|---|
| CI job (keyless) | a short-lived Sigstore certificate for its OIDC identity | runner |
| developer | a local ECDSA P-256 key file | developer |

Your policy sets the level. The level the result claims is ignored.

--8<-- "docs/trust-model.md:trust-levels"

## The policy

A policy is a YAML file in your repository. This one trusts the `main`
workflow at runner level and Alice's key at developer level:

--8<-- "docs/trust-model.md:policy-file"

With `minTrustLevel: runner`, a result Alice signed is a miss: the test runs.
Without `--policy`, nothing is trusted and every test runs.

## What a signature proves

- **Proves:** who signed the result, and that nobody changed it since.
- **Does not prove:** that the test ran honestly, or that inputs outside the
  contract were the same.

So only grant a level to signers you would trust to run the test yourself.

Details: [the trust model](../trust-model.md) (keyless verification, every
policy field, threats).
