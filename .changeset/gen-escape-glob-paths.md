---
"aval": patch
---

`aval gen`: file and directory names with glob metacharacters (`*`, `?`, `[`) are escaped in the generated scope, so a unit in such a directory keeps its own files in scope instead of silently matching nothing. A name no contract glob can express (a backslash) fails closed (exit 1, nothing written).
