---
"aval": minor
---

New optional `environment.probe` in `aval.yaml` (additive to `aval.trianalab.io/v1`; older `aval` binaries reject contracts that use it): a map from an `environment.toolchain` key to an argv such as `python: [python3, --version]`. `aval run` runs each probe before the lookup, without a shell, with a 10 s timeout and without `$SIGSTORE_ID_TOKEN`, and requires the declared value in the output as a version token (`3.13` matches `Python 3.13.1`, not `Python 3.130`). A mismatch, a probe that cannot start, a non-zero exit, a timeout or output over 4 KiB skips the lookup (outcome `error`), runs the test, records and publishes nothing and exits 1. The observed output goes into `execution.environment` as `probe.<key>`. `verify`, `push`, `--fingerprint-only` and the browser demo never run probes. Fix: a toolchain key named `os` or `arch` no longer overwrites the real values in `execution.environment`.
