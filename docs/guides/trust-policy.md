# Write a trust policy

Goal: a `policy.yaml` that says whose results you reuse, and nobody else's.

```mermaid
flowchart LR
  p([policy.yaml]) --> id["identities:<br>issuer + subjectRegexp,<br>or publicKey"] --> tl[trustLevel] --> min{"≥ minTrustLevel?"}
  min -- yes --> ok([reuse]):::ok
  min -- no --> m([miss]):::muted
```

The full decision, signature included: [Trust](../concepts/trust.md).

## The example

Trust one GitHub Actions workflow on `main`. This is the policy of the
`example-go-compose` job in Aval's CI:

```yaml
--8<-- "examples/go-compose/policy.yaml"
```

For a key, see [Sign results](signing.md#the-example). Use it with
`--policy`, or set `AVAL_POLICY` once (a flag wins over the variable):

```sh
aval verify --registry ghcr.io/acme/orders-aval --policy policy.yaml
```

## The fields

--8<-- "docs/trust-model.md:policy-fields"

## Tips

- Anchor the subject to one workflow file and one branch. A pull request
  branch can run any code, so do not trust it at `runner` level.
- Give developer keys `developer`; keep `minTrustLevel: runner` in CI so CI
  never reuses laptop results.

??? info "How the subject is matched"
    `subjectRegexp` must match the whole subject, as if it started with `^`
    and ended with `$`. Escape dots: `github\.com`. For GitHub Actions the
    subject is `https://github.com/<owner>/<repo>/<workflow path>@<ref>`.

??? info "Keyless checks and the network"
    A keyless policy fetches the Sigstore trusted root on first use and caches
    it under `$HOME/.sigstore`. Offline, that candidate is an `error` and the
    test runs. Pin the root with `trustedRoot` to avoid this; see
    [the trust model](../trust-model.md#keyless-verification-needs-the-network-and-a-writable-home).

## Read next

- [Troubleshooting](troubleshooting.md#errors-exit-1) lists every policy error.
- [Trust](../concepts/trust.md): the ideas behind levels
- [Trust model: policy file](../trust-model.md#policy-file)
