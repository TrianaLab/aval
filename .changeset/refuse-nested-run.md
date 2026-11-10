---
"aval": patch
---

`aval run` no longer loops without end when `runner.command` starts `aval run`
on the same contract again, directly or through other contracts (#196). It
passes `AVAL_RUN_STACK` to `runner.command`, and a nested run of a contract
already in it exits 2; the outer run records the test as failed (exit 3). A
nested run of a different contract still works. `environment.env` may no
longer list `AVAL_RUN_STACK`. No format changes: the fingerprint, predicate
and verify report are unchanged.
