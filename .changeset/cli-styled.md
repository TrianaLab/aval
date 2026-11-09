---
"aval": minor
---

`aval run` on a terminal prints the styled output of Epic 13 (style A): one line per step behind a glyph (`✓` done, `↺` reused, `✗` failed or SECURITY, `⚠` reuse off, `·` neutral), labelled detail lines, digests as 8 hex characters, and one closing sentence with the next step. After a verification failure nothing is green and no `✓` appears; the SECURITY block goes to stderr when found, and stderr still ends with the plain `aval: SECURITY:` line. `NO_COLOR` drops the colour only. Plain output is unchanged and applies when stdout is not a terminal, with `TERM=dumb`, `AVAL_OUTPUT=plain` or the new global `--plain` flag. Exit codes, `-o json`, the report file and `aval verify` are unchanged. The browser demo uses the styled form without colour.
