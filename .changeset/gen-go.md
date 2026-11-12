---
"aval": minor
---

`aval gen go`: the Go adapter for `aval gen`. One unit per package of the main modules under the contract root, read with `go list -deps -test -json`: the package's `.go` files, `go:embed` files, non-Go sources and `testdata`, and its imports of other local packages from every `.go` file whatever its build tags. `--go-cover` makes each test fail below 100% statement coverage of its package (docs: Guides, Generate scope from the dependency graph).
