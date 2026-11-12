---
"aval": minor
---

`aval gen go` writes the shared fragment `go.module` and every generated `go.*` test uses it: the Go version from the `toolchain` (else `go`) directive of `go.work` or `go.mod` with a `go env GOVERSION` probe, `go.work`, `go.work.sum` and each main module's `go.mod` and `go.sum`, and the Go build environment (`GOFLAGS`, `GOOS`, `GOARCH`, `CGO_ENABLED`, ...). A toolchain bump is drift that `--check` reports; a version it cannot read is an error. Remove the hand-written Go toolchain and probe from `defaults`: a different value there now conflicts (docs: Guides, Generate scope from the dependency graph).
