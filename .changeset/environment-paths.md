---
"aval": minor
---

`environment.paths` declares files and directories outside the tree as
fingerprint inputs (#254), such as `~/.config/pacto` or
`${XDG_CONFIG_HOME}/pacto`. Each entry adds a `path <entry> <hash>` manifest
line with the entry as written, so another `$HOME` with the same content
shares a result; `absent` and `unset` are hashed too. A symlink, a special or
unreadable file fails the fingerprint, and an entry inside the contract tree
is a contract error. `aval explain` lists the lines. Format change: an
additive optional contract field under the same `apiVersion`, with a manifest
line kind only it brings; the manifest stays `aval-fingerprint/v2` and
existing contracts hash as before. An older `aval` rejects a contract that
uses `paths` (unknown field), which fails closed.
