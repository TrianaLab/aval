---
"aval": minor
---

The mutable-reference scanner now reports container images named in test code that uses testcontainers: a quoted `name:tag` without a digest in a `*.go`, `*.py`, `*.java`, `*.kt`, `*.ts`, `*.js` or `*.cs` file that mentions testcontainers. Adopters may see new findings that turn hits into misses for `hermetic` and `controlled-environment` tests: pin the image by digest, or accept it in `scope.allowMutable`.
