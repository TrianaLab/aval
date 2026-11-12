---
"aval": patch
---

`aval gen rust`: a contract root reached through a symlink (a clone under macOS `/tmp`, a symlinked working directory) is no longer rejected with "outside the contract root". The root is made absolute before its symlinks are resolved, so it matches the canonical paths cargo prints; a member that really is outside the root still fails.
