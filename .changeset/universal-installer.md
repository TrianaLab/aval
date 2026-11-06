---
"aval": minor
---

One-line installers: `scripts/get-aval.sh` (Linux and macOS, `curl | bash`) and `scripts/install.ps1` (Windows, `irm | iex`). Both refuse a download whose sha256 does not match the release's `checksums.txt`, and verify the release signature (cosign) and SLSA provenance (`gh attestation verify`) when those tools are installed. See docs/install.md.
