---
"aval": patch
---

`run --push` and `push` check their preconditions before anything runs and exit 2 (usage) instead of failing with exit 1 after the test: a `--key` that cannot be read or is not an unencrypted ECDSA P-256 PEM key (the error names the requirement and the `openssl` command to create one; cosign keys are encrypted and not supported), or no `--key` and no `$SIGSTORE_ID_TOKEN`. `run`, `verify` and `push` reject a `--registry` that is not an OCI repository (a scheme, no repository, a tag or a digest) with exit 2 and an example. A `--result` or `--report` path that the contract scope includes and that holds a real input (anything other than an Aval result or report) exits 2 before the test runs, because skipping it would drop that input from the fingerprint. Exit codes keep their values; only these cases move from exit 1 to the existing exit 2.
