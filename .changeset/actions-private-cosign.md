---
"aval": patch
---

The GitHub actions no longer put cosign on the job's `PATH`. They install the
cosign that verifies the aval release into `$RUNNER_TEMP`, checked against a
per-platform sha256, instead of using `sigstore/cosign-installer`, which
writes `GITHUB_PATH`. The test and later steps see the job's own tools again.
