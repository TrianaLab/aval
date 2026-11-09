---
"aval": patch
---

`aval upgrade` names the HTTP status when GitHub answers the latest-release lookup with something other than a redirect (for example `504 Gateway Timeout` during a GitHub incident), instead of `no Location header in response`. Nothing is downloaded or replaced in that case, as before.
