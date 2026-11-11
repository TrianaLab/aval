---
"aval": minor
---

A TestSet `matrix` now substitutes `{var}` into `environment.toolchain`
values, so one test can declare a value per leg (for example
`platform: "{os}"`) and each leg gets its own fingerprint. An unknown
`{var}` in a toolchain value, or a `{var}` in a toolchain value of a test
without a matrix, is now a contract error (exit 2); before, it was hashed as
literal text. Contracts without `{var}` in toolchain values keep their
fingerprints.
