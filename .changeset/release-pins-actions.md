---
"aval": patch
---

After each release, the release workflow opens a PR that moves the aval
version and per-platform sha256 pinned in `integrations/github/run` and
`integrations/github/publish` to that release, from its `checksums.txt`
(#213). Pinning either action by commit SHA pins the aval binary too, and one
Dependabot bump upgrades both. `tests/release` fails when a pin does not
match the pinned release's `checksums.txt`. No change to the aval binary,
its exit codes or formats.
