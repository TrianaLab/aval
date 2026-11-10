# Write a trust policy

Goal: a `policy.yaml` that says whose results you reuse, and nobody else's.

```mermaid
flowchart LR
  p([policy.yaml]) --> id["identities:<br>issuer + subjectRegexp,<br>or publicKey"] --> tl[trustLevel] --> min{"≥ minTrustLevel?"}
  min -- yes --> ok([reuse]):::ok
  min -- no --> m([miss]):::muted
```

## The example

Trust one GitHub Actions workflow on `main` (the policy of Aval's
`example-go-compose` job):

```yaml
--8<-- "examples/go-compose/policy.yaml"
```

For a key: [Sign results](signing.md#the-example). Pass it with `--policy`,
or set `AVAL_POLICY` once (the flag wins):

```sh
aval verify --registry ghcr.io/acme/orders-aval --policy policy.yaml
```

To skip `--registry`, add `registry: ghcr.io/acme/orders-aval` to the
policy. The flag and `AVAL_REGISTRY` still win, and the `config:` line says
which one was used. Never put `user:password@` in it: logins come from
`docker login`.

## One policy for PRs and main

A policy that also trusts developer keys sets `minTrustLevel: developer`, so
PRs reuse them. On `main`, raise the minimum for that run only:

```sh
aval run --policy policy.yaml --min-trust-level runner
```

`AVAL_MIN_TRUST_LEVEL=runner` does the same (the flag wins). Both work on
`aval run` and `aval verify` and can only raise the policy's minimum: a lower
or unknown level exits 2. A developer result is then a miss (exit 4), never
exit 5. A policy that trusts only main's CI identity does not need this.

## The fields

--8<-- "docs/trust-model.md:policy-fields"

## Tips

- Read the policy from a protected branch in CI, never from the PR's checkout
  ([why](../trust-model.md#take-the-policy-from-a-protected-branch)): its
  identities and its `registry` decide what is trusted.
- Anchor the subject to one workflow file and one branch. A PR branch runs
  any code: never trust it as `runner`.
- Give developer keys `developer`. Keep `minTrustLevel: runner` in CI, so CI
  never reuses laptop results.
- `subjectRegexp` must match the whole subject. Escape dots: `github\.com`.
- Keyless checks fetch the Sigstore root on first use and cache it under
  `$HOME/.sigstore`; offline the test runs. Pin
  `trustedRoot` to avoid it ([details](../trust-model.md#keyless-verification-needs-the-network-and-a-writable-home)).

## Read next

- [Trust](../concepts/trust.md)
- [Troubleshooting](troubleshooting.md#errors-exit-1): every policy error
