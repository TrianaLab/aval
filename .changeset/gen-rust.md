---
"aval": minor
---

`aval gen rust`: one unit per Cargo workspace member, read with `cargo metadata --no-deps`, with path dependencies between members as edges and `cargo test -p <member>` as the test (docs: Guides, Generate scope from the dependency graph).
