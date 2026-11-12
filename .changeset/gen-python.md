---
"aval": minor
---

`aval gen python`: one test per local Python project (uv workspace members, poetry and pip projects) with edges from declared dependencies and local imports, read by Python's own `tomllib` and `ast`. `--strict-imports` fails on a dynamic import it cannot resolve (docs: Guides, Generate scope from the dependency graph).
