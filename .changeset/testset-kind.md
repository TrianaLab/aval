---
"aval": minor
---

New contract kind `kind: TestSet` (#206): many tests in one file, with
`defaults`, named `fragments` a test pulls in with `use`, and a `matrix`
that creates one test per value. Merge order is defaults, the used
fragments, then the test; lists are unioned, so nothing inherited is
removed, and a toolchain, probe or `scope.root` key set to two values exits
2 naming both sources. `matrix` substitutes `{var}` into `metadata.name` and
`runner.command` only. Each test expands to a standalone `kind: Test` and
fingerprints byte for byte like it. Unknown or unused fragments, a fragment
that uses another or sets `exclude`, `runner`, `metadata`, `outputs`,
`matrix` or `cacheability`, an unknown `{var}`, duplicate expanded names and
a `scope.contracts` entry that points at a TestSet exit 2. `aval run`,
`explain`, `verify` and `push` take a TestSet of one test; selecting one test
of many comes with #207.

Format changes: additive new kind under `aval.trianalab.io/v1`. Older
`aval` binaries reject `kind: TestSet` with exit 2 (fail closed). The
manifest version (`aval-fingerprint/v2`) does not change.
