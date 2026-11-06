---
"aval": patch
---

The fingerprint no longer hashes Aval's own outputs: `.aval` directories at any depth, and the `--result` and `--report` paths of the command when they fall inside the contract root. A broad scope such as `scope.source: ["**"]` now keeps its fingerprint across `aval run`s, and `aval push` accepts the result `aval run` recorded. The manifest format stays `aval-fingerprint/v1`: trees with no Aval output in scope keep byte-identical manifests and digests (the committed v1 golden and Aval's own contracts are unchanged). Only manifests that listed a file under `.aval/` or a `--result`/`--report` file change, to the manifest of the same tree without those files. Such a manifest held a result from an earlier run, so it changed on every run and was not reusable in practice; results published from a clean checkout (no Aval output yet) keep their fingerprint and stay reusable. `.aval/` is now reserved for Aval: a file the test reads must not live there.
