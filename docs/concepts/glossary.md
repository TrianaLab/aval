# Glossary

What does this word mean? Hover a dotted word anywhere in the docs for the short form.

| Term | Meaning | More |
|---|---|---|
| Attestation | a signed statement: test X, with inputs that hash to fingerprint F, gave result R | [artifact format](../artifact-format.md) |
| Cacheability | how safe a test is to reuse: `hermetic` and `controlled-environment` yes, `external-dependencies` only if the policy allows it, `non-deterministic` never | [classes](../contract.md#cacheability-classes) |
| Contract | `aval.yaml`: what the test is, how to run it, which inputs it reads | [the test contract](../contract.md) |
| Fingerprint | one `sha256` of every declared input: the contract (command included), files in scope, toolchain, env var values, resolved references. Same inputs, same fingerprint | [what goes in](../contract.md#what-goes-into-the-fingerprint) |
| Mutable reference | a tag or range that can point at other content later, such as `postgres:16`. Blocks reuse for hermetic and controlled-environment tests until you pin it or list it in `scope.allowMutable` | [Pin your dependencies](../pinning.md) |
| Outcome | the result of a lookup: `verified`, `miss`, `verification-failed` or `error`. Only `verified` skips the test | [Outcomes](outcomes.md) |
| Policy | your YAML file of trusted signers and their levels. No policy, no reuse | [Trust](trust.md#the-policy) |
| Registry | the OCI registry that stores signed results, one you already run | [artifact format](../artifact-format.md) |
| Scope | the inputs the contract lists: `source`, `dependencies` (such as `go.mod`), `resolve` and `contracts`. Aval sees nothing else | [the test contract](../contract.md#fields) |
| Signer | who signed a result: a CI job (keyless, OIDC) or a key | [Trust](trust.md#signers-and-levels) |
| Trust level | how much the policy trusts a signer: developer, runner, isolated. Reuse needs at least `minTrustLevel` | [Trust](trust.md#signers-and-levels) |
