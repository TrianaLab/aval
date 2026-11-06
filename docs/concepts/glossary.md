# Glossary

Each word Aval uses with a special meaning, defined once. Across the docs,
hover a dotted word to see its short form.

## Attestation

A signed statement: "test X, with inputs that hash to fingerprint F,
produced result R". Aval stores it in your OCI registry. See
[the artifact format](../artifact-format.md).

## Cacheability

How safe a test is to reuse, declared in the contract. `hermetic` and
`controlled-environment` are reusable. `external-dependencies` is reusable
only if the policy allows it. `non-deterministic` is never reusable. See
[cacheability classes](../contract.md#cacheability-classes).

## Contract

The `aval.yaml` file next to a test. It says what the test is, how to run
it, and which inputs it reads. See [the test contract](../contract.md).

## Fingerprint

One `sha256` hash of every input the contract declares: the contract
itself (the command included), the files in scope, and the declared tool
versions and environment variables. Same inputs, same fingerprint. See [How Aval works](index.md).

## Outcome

The decision on a lookup: `verified`, `miss`, `verification-failed` or
`error`. Only `verified` skips the test. See [Outcomes](outcomes.md).

## Policy

A YAML file you write that lists the signers you trust and the trust level
each one gets. Without a policy, nothing is reused. See [Trust](trust.md).

## Scope

The files the contract lists as test inputs: `source` files and
`dependencies` such as `go.mod`. Aval sees nothing outside it. See
[what goes into the fingerprint](../contract.md#what-goes-into-the-fingerprint).

## Signer

The identity behind a signature: a CI job's OIDC identity (keyless), or a
key. See [Trust](trust.md#signers).

## Trust level

How much the policy trusts a signer: 1 developer, 2 runner, 3 isolated. A
result is reused only if its signer's level is at least the policy's
`minTrustLevel`. See [Trust](trust.md#trust-levels).
