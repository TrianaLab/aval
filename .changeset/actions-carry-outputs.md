---
"aval": patch
---

The GitHub actions carry a test's `outputs` from run to publish (#236).
`aval run --outputs-dir DIR` copies the files the `outputs` globs matched
into an empty DIR when it records the result, and `aval push --outputs-dir
DIR` publishes them from there instead of the contract root, refusing
(exit 1, nothing published) any entry that is not a regular file matched by
an `outputs` glob: a symlink, an extra file, an input, `.git` or `.aval`.
The run action passes `--outputs-dir .aval/<test>/outputs` on a push to the
default branch; the publish action passes the artifact's `outputs/`, so a test with outputs and no `outputs/` exits 1 instead of
publishing the checkout's files, and it refuses an artifact with any entry
outside `result.json`, `report.json`, `record.json` and `outputs/`. docs/guides/github-actions.md
documents the artifact layout. No format change. This closes the known gap
noted in 0.8.1 (the publish action could not push a test with `outputs`).
With a pinned release without `--outputs-dir` (0.8.1), the actions keep
running and publishing tests without `outputs`, and refuse a test with
`outputs`, as `aval explain --expand` reports it, with exit 1 naming aval
v0.8.2 or later, instead of dropping its outputs or signing the checkout's
files.
