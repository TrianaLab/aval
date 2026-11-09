---
"aval": minor
---

New `aval explain [--digest]`: it prints `fingerprint manifest for <test> (aval-fingerprint/v1)`, the manifest, and `fingerprint sha256:...` as the last line; `--digest` prints only the digest. It never looks anything up and never runs the test; a fingerprint error exits 1 with `aval: fingerprint: ...`, as `run --explain` does. `run --explain` and `run --fingerprint-only` keep their behaviour. New `aval stats publish FILE` publishes a run record and replaces `aval record`. Flags are named for what they hold; the old names keep working, hidden from help, and print a deprecation notice on stderr only (stdout, files and exit codes are unchanged). Deprecated in 0.7.0, removable no earlier than 0.8.0:

- `aval run --result` and `aval push --result`: use `--result-file`
- `aval run --report`: use `--report-file`
- `aval run --record`: use `--stats`
- `aval run --record-file`: use `--stats-file`
- `aval run --repository` and `aval record --repository`: use `--source-repo`
- `aval record --record-file FILE`: use `aval stats publish FILE`

Messages that name these flags use the new names (for example `--stats requires --registry`), and `aval record` without a file now says `a run record file is required`. The browser demo accepts `aval explain` and `aval explain --digest`.
