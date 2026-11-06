# Sign with a key or keyless

Goal: sign a test result so that others can check who published it.

```mermaid
flowchart LR
  run([aval run]) --> result[.aval/result.json] --> push[aval push]
  push -- key file --> keyed([key-based]):::muted
  push -- OIDC token --> keyless([keyless]):::ok
```

## The example

=== "Key"

    Make a P-256 key and a policy that trusts it:

    --8<-- "README.md:key"

    Run the test, then sign and push the result:

    --8<-- "README.md:push"

    This is the [quickstart](https://github.com/TrianaLab/aval#try-it-on-your-laptop)
    path; a test runs it as written.

=== "Keyless"

    Put an OIDC token in `$SIGSTORE_ID_TOKEN` and leave out `--key`:

    ```sh
    export SIGSTORE_ID_TOKEN="<OIDC token for audience sigstore>"
    aval run --push --registry ghcr.io/acme/orders-aval
    ```

    On GitHub Actions the job gets the token itself; see
    [Use Aval in GitHub Actions](github-actions.md).

## Which one

| | Key | Keyless |
|---|---|---|
| Who signs | whoever holds the key file | a CI workload or a person, proven by an OIDC token |
| Policy entry | `publicKey: aval.pub` | `issuer` and `subjectRegexp` |
| Typical trust level | `developer` | `runner` |
| Needs network to verify | no | yes, unless the policy pins `trustedRoot` |

Use keyless in CI. Use a key on your laptop or in a closed network.

??? info "Which keys work"
    Aval signs with an unencrypted ECDSA P-256 private key in PEM (PKCS#8 or
    SEC1). Cosign keys are encrypted and do not work. Make one with
    `openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out aval.key`.

??? info "Sign later with aval push"
    `aval run` records the result in `.aval/result.json`. `aval push` signs and
    publishes it later, with the same `--key` or token. It refuses a result
    whose inputs no longer match the contract:

    ```sh
    aval run --registry ghcr.io/acme/orders-aval --policy policy.yaml
    aval push --registry ghcr.io/acme/orders-aval
    ```

??? info "What a signature proves"
    A signature proves who published the result, not that the test ran
    honestly. That is why the policy decides how much each signer is trusted.
    See [Trust](../concepts/trust.md).

## Read next

- [Write a trust policy](trust-policy.md)
- [Trust model: signing](../trust-model.md#signing)
