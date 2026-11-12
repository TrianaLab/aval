---
"aval": minor
---

`aval gen go` adds to a package's fragment the files its code and tests read by a path it can resolve, such as `os.ReadFile(filepath.Join("../../fixtures", "order.json"))`: literals, constants, `filepath.Join`, variables, table tests, the package's own helpers and `runtime.Caller(0)`, through `os`, `ioutil`, `filepath`, `template.ParseFiles`/`ParseGlob` and `http.Dir`. A read it cannot resolve that may leave the package directory (including paths from parameters it cannot follow, other packages' functions and variables, methods, and a `Chdir` to a computed directory), a path outside the contract root, or a relative path in library code fails with the file and line; `--go-allow-dynamic <pattern>` accepts them for the matching packages, whose authors declare those files by hand (docs: Guides, Generate scope from the dependency graph).
