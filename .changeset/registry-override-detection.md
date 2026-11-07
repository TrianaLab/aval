---
"aval": patch
---

`scope.resolve` detects registry overrides as the tools read them. pip variables match in any spelling pip reads (`PIP_` then any letter case, `-` equal to `_`, one optional leading `--`: `PIP_index-url`, `PIP___INDEX_URL`). `~/.npmrc` is read as ini, like npm: quotes stripped from keys and values, `;` and `#` comments, keys under a `[section]` ignored, `${VAR}` expanded in keys and values as npm does (an undefined variable stays literal); a line Aval cannot read (including one with U+2028 or U+2029, or a quoted key or value that is JSON other than a string) fails the fingerprint and sends no token. A floating Maven version fails when `<latest>` or `<release>` is a SNAPSHOT or not in `<versions>`. Errors show only the host of a URL, so a token in a registry path (`https://npm-proxy.fury.io/<token>/user/`) is masked too.
