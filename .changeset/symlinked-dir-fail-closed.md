---
"aval": patch
---

A symlink in the contract directory whose name no glob matches, but under which a `source` or `dependencies` glob could match files (for example `pkg/sub -> /elsewhere` with `**/*.go`), now fails the fingerprint with an error naming the path. Before, it was skipped, so edits behind it kept the same fingerprint and a stale result could be reused. A symlink no glob can reach is still ignored, and a contract directory that is itself a symlink fingerprints like its target.
