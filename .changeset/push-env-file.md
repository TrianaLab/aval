---
"aval": patch
---

`aval push` accepts a result from a job with other environment variables
(#247). `aval run` writes an env file next to the result (`--env-file`,
default `.aval/env.txt`, `.aval/<test>/env.txt` with `--test`): the
fingerprint manifest version, then the manifest's `env NAME unset|<sha256>`
lines, hashes only, neither signed nor published. `aval push --env-file
FILE` takes the `env` section from it instead of its own environment; every
source, dependency, toolchain and contract line is still hashed from the
checkout, and the digest must still match the result and the report. A file
with another manifest version, a malformed or repeated line, or names other
than `environment.env` exits 1 and publishes nothing; inside the contract
scope and outside `.aval/` it exits 2, as `--result-file` does. Without the
flag push is unchanged. The publish action passes the artifact's `env.txt`
when the installed aval has the flag, so a variable such as
`GOTOOLCHAIN=local` from setup-go no longer fails the push. No format
change: the manifest stays `aval-fingerprint/v2`. docs/artifact-format.md
documents the env file.
