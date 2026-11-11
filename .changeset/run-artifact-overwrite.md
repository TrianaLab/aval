---
"aval": patch
---

The run action uploads `aval-<test>` with `overwrite: true`. Re-running a
failed job no longer fails with 409 on the earlier attempt's artifact, and the
publish job signs the result of the attempt that just ran. Run each test in
only one job per workflow run.
