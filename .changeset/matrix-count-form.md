---
"aval": minor
---

A TestSet matrix variable can be `{count: N}` (N from 1 to 1024): it takes
the values "1" to "N" and defines `{v_count}` = N, so a sharded runner
writes `PW_SHARD={shard}/{shard_count}` and the total is stated once. The
expanded tests and their fingerprints are the same as the list form. A bad
count, an extra key in the mapping, or a `v_count` variable beside a count
variable `v` is a contract error (exit 2). Additive within
`aval.trianalab.io/v1`: older aval binaries reject the mapping form.
