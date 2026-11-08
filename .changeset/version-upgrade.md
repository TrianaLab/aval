---
"aval": minor
---

New `aval version` (prints `aval <version> (<go version> <os>/<arch>)`) and `aval upgrade [--version vX.Y.Z] [--check]`. `upgrade` resolves the latest release from the GitHub Releases redirect (no API call), verifies the keyless Sigstore signature of `checksums.txt` against Aval's release workflow at that tag, checks the archive's sha256 against its exact entry, and only then replaces the running binary atomically on Unix (on Windows the old one is kept as `aval.exe.old` and moved back if the swap fails). Without `--version` it refuses to move to an older release. Any failed check replaces nothing and exits 1; downloads are capped at 256 MiB (1 MiB for `checksums.txt` and its bundle).
