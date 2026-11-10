---
"aval": minor
---

`aval run` and `aval verify` take `--min-trust-level` (or
`AVAL_MIN_TRUST_LEVEL`) to raise the trust policy's `minTrustLevel` for one
run (#214), so one policy can trust developer keys on PRs and require
`runner` on main. A value below the policy's minimum, or an unknown level,
exits 2. A result below the raised minimum is a miss (exit 4), never exit 5.
The verify report is unchanged. `runner.command` does not see
`AVAL_MIN_TRUST_LEVEL`.
