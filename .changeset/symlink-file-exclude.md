---
"aval": patch
---

A symlink to a regular file under a `**` source glob now needs only its name
in `exclude` (#258); `<link>/**` is no longer required, since no path exists
under a file. A link to a directory, a dangling link or a loop still needs
both. The link is never followed nor hashed; Aval only checks its target's
type. The error names the exclude that would fix it. Dropping `<link>/**`
from a contract changes its hash, so the next run is a miss.
