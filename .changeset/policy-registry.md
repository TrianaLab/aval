---
"aval": minor
---

The trust policy takes an optional `registry` field (#209). `aval run` and
`aval verify` use it when neither `--registry` nor `AVAL_REGISTRY` is set, and
the `config:` line names the source that won (`registry=<x> (policy <file>)`).
Reading the policy from a protected branch now carries the registry too.
A policy `registry` with credentials (`user:password@`) exits 2 without
printing them.

Format change (additive): `kind: Policy` gains an optional field under the
same `apiVersion`. Older aval releases reject a policy that uses it (unknown
field), which fails closed; policies without it work everywhere.
