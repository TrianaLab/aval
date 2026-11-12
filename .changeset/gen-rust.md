---
"aval": minor
---

`aval gen rust`: one unit per Cargo workspace member, read with `cargo metadata --no-deps`, with path dependencies between members as edges and `cargo test -p <member>` as the test. A `[patch]` or `.cargo/config.toml` `paths` override outside every member fails closed (docs: Guides, Generate scope from the dependency graph).
