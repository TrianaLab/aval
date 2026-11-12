---
"aval": patch
---

`aval gen`: a `kind: TestSet` with old Mac (`\r`) line endings, or a stray `\r`, `U+0085`, `U+2028` or `U+2029` line break, no longer makes the rewrite panic: lines are counted the way the YAML parser counts them. Generated lines follow the file's line endings, so a `\r\n` file round-trips byte for byte.
