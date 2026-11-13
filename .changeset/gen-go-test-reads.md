---
"aval": minor
---

`aval gen go` puts what a package's test files read in a second fragment, `go.<package>.test`, that only the package's own test uses: the tests of the packages that import it no longer re-run, nor lose reuse, when a fixture only its tests read changes. What its non-test code reads stays in `go.<package>`. A bare `.`, `..`, `./` or `../` in a test's argument or table row (an invalid name, not a path) no longer adds the parent directory; `os.ReadDir("..")` and the other reads still do. A package under `--no-test` keeps its test reads in `go.<package>` (docs: Guides, Generate scope from the dependency graph). Rerun `aval gen go` after the upgrade; `--check` reports the moved entries until then.
