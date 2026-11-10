---
"aval": minor
---

The installers can pin and require verification for CI (#202).
`get-aval.sh --sha256 <hex>` (or `AVAL_SHA256`; `<os>_<arch>=<hex>` per
platform, the entry for the detected platform required) refuses an archive
with another digest or one that `checksums.txt` does not list with it.
`--require-verify` (`AVAL_REQUIRE_VERIFY=1`) makes a missing `cosign` an
error, `--require-provenance` (`AVAL_REQUIRE_PROVENANCE=1`) does the same for
`gh`; both need `--version` and exclude `--no-verify`. `install.ps1` reads the
same variables. Interactive installs behave as before. `docs/install.md`
has a CI recipe, and Aval's own CI installs its pinned release this way.
