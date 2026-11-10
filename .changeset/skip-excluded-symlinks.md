---
"aval": patch
---

The fingerprint no longer refuses a symlink that `scope.exclude` covers by
name and contents, such as `node_modules/.bin/tool` under `pkg/**` with
`**/node_modules/**` excluded (#198). The symlink is skipped, not followed,
and stays out of the manifest. A symlink a `dependencies` glob reaches, or
one excluded only by name or only by its contents, is still an error. No
format changes: every tree that fingerprinted before keeps its fingerprint.
