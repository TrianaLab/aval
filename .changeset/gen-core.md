---
"aval": minor
---

`aval gen <language>`: the language-agnostic core that writes one fragment and one test per unit from a dependency graph into a `kind: TestSet`, with the transitive closure computed by Aval, stable `<language>.<unit>` names, hand-written entries kept byte for byte, and `--check` for CI. No language adapter ships yet (docs: Guides, Generate scope from the dependency graph).
