---
"aval": patch
---

`aval gen js`: TypeScript project `references` and `extends`, and relative imports that cross into another workspace package, are now edges, so a test is no longer reused when a package it reaches only that way changes. A root `tsconfig.base.json` a package extends joins its files. A relative import outside every workspace package, or one that cannot be resolved, fails closed with the file and line (docs: Guides, Generate scope from the dependency graph).
