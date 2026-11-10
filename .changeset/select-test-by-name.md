---
"aval": minor
---

`-t/--test <name>` on `aval run`, `explain`, `verify` and `push` selects one
test of a contract file (#207). A TestSet of more than one test without
`--test` exits 2 and lists the names; on a `kind: Test` the flag is optional
and must equal `metadata.name`. With `--test` the output files default to
`.aval/<name>/{result,report,record}.json`, and `aval run` removes a stale
`.aval/<name>/` first; without it nothing changes. `aval explain --test
<name> --expand` prints the expanded standalone `kind: Test`, and
`aval explain --all [-o json]` fingerprints every test without running
anything, reports every failing test by name and exits 2 (or 1). A TestSet
expands to at most 1024 tests in total, plain tests included.

Format changes: additive CLI flags only. No new exit code; the verify report,
predicate and manifest version do not change.
