---
"aval": minor
---

`scope.optionalSource` declares source globs that may match no file yet
(#257), for inputs a test discovers by pattern such as `**/*testdata*.yaml`.
Matching files are hashed as `source` lines, with the same `exclude` rules,
so adding the first one is a miss. A glob may not be in both `source` and
`optionalSource`, and `source` plus `optionalSource` must still hash at least
one file. `aval explain` names optional globs that matched nothing on
stderr. Format change: an additive optional contract field under the same
`apiVersion`; the manifest stays `aval-fingerprint/v2`. Existing contracts
hash as before; an older `aval` rejects a contract that uses
`optionalSource` (unknown field), which fails closed.
