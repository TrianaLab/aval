---
"aval": minor
---

`aval gen <language> --no-test <pattern>`: write no test for the units whose ID matches a contract glob (repeatable), for every language. Their fragment stays while another test uses it, so their files stay in that test's scope; a pattern that matches no unit is a usage error (exit 2), and `--check` compares the output of the same flags (docs: Guides, Generate scope from the dependency graph).
