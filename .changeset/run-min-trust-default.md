---
"aval": minor
---

The GitHub run action raises the trust minimum to `runner` on the default
branch (#251), so `main` no longer goes green on a developer-signed result that
`aval push` would then not publish. The new `min-trust-level` input (`auto` by
default, or `developer`, `runner`, `isolated`) passes the highest of itself,
the job's `AVAL_MIN_TRUST_LEVEL` and the policy's `minTrustLevel`, so it never
lowers the policy's minimum; `min-trust-level: developer` opts out. A policy
minimum of `isolated` is kept instead of failing with exit 2, and the log and
step summary show the effective minimum. Workflows can drop their hand-written
`AVAL_MIN_TRUST_LEVEL` expression.
