---
"aval": patch
---

`aval run` no longer exits 1 when an `environment.probe` fails without
`--push` (#197). It still looks nothing up (outcome `error`), runs the test
and records nothing to `--result-file`, so `aval push` cannot publish the
result; it now warns `result not recorded: <reason>` and exits with the
test's code (0 passed, 3 failed). With `--push` it still exits 1
(`result not published: <reason>`), or 3 when the test failed. No new exit
code or outcome; the verify report, predicate and fingerprint are unchanged.
