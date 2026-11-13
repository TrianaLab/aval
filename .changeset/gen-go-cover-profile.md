---
"aval": minor
---

`aval gen go --go-cover-profile <dir>`: each generated test writes its package's coverage profile to `<dir>/<test>.out` (`go test -coverprofile`, no `-coverpkg`) and declares it in `outputs`, so a reused result restores it. A project's coverage gate merges the profiles with no test re-run; merged, they equal one `go test -coverprofile ./...` over the same packages. It composes with `--go-cover`. A profile path inside a package's scope is an error (docs: Guides, Generate scope from the dependency graph).
