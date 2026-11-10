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
default branch; the publish action passes it when the artifact has
`outputs/` and refuses an artifact with any entry outside `result.json`,
`report.json`, `record.json` and `outputs/`. docs/guides/github-actions.md
documents the artifact layout. No format change.
