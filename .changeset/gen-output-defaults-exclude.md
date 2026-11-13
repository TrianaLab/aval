---
"aval": patch
---

`aval gen`: an output, such as a `--go-cover-profile` directory, is checked against the effective scope of each test the TestSet holds after the rewrite, not against the raw globs of every unit. A path `defaults.scope.exclude` (or a test's own exclude) removes from a test's sources is accepted, so a unit whose scope is `**` no longer makes every profile directory fail; a `--no-test` unit's files count only when a written test uses its fragment. Dependencies still count, and a path in a hand-written test's scope is now an error too (docs: Guides, Generate scope from the dependency graph).
