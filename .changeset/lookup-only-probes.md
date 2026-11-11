---
"aval": patch
---

Docs: a `lookup-only` step must not skip setup that `environment.probe` checks, since the run step probes before its lookup; the example now gates `npm ci` and the browsers instead of `setup-node`.
