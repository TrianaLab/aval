---
"aval": minor
---

The fingerprint skips every entry named exactly `.git`, whatever its type
(#199). A linked worktree or a submodule has a `.git` file that holds a
machine-specific path; a `**` source glob used to hash it, so the main
checkout, each worktree and each machine got a different fingerprint for the
same commit and scope. They now share one, and the `exclude: ["**/.git"]`
workaround is no longer needed. A `.git` symlink is skipped instead of failing
the fingerprint. The match stays case-sensitive (`.GIT` is still an input). An
exact glob such as `source: ["pkg/.git"]` now matches no file and fails the
fingerprint (exit 1, `matched no files`). `outputs` skip a `.git` file too, so
it is never published.

Format changes: the fingerprint manifest moves from `aval-fingerprint/v1` to
`aval-fingerprint/v2`. Every existing attestation becomes a miss once, so each
test runs again and publishes a v2 result; there is no fallback lookup by v1.
