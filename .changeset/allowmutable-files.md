---
"aval": minor
---

`scope.allowMutable` entries take an optional `files` list of globs (#208).
One entry accepts its exact `ref` in every file in scope a glob matches,
instead of one entry per file. `files` excludes `file`, which stays an exact
path; each glob must match a file in scope, otherwise `aval` exits 2. Format
change: an additive optional contract field under the same `apiVersion`.
Existing contracts hash as before; an older `aval` rejects a contract that
uses `files` (unknown field), which fails closed.
