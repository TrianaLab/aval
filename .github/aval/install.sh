#!/bin/bash
# Usage: install.sh (reads AVAL_VERSION and AVAL_SHA256, pinned in ci.yaml)
# Installs the pinned aval release (linux/amd64) on PATH with main's
# scripts/get-aval.sh (#202), so CI and users share one installer:
# --sha256 pins the archive, so a re-tagged release cannot change what runs,
# and checksums.txt must list that sha256; --require-verify makes a missing
# cosign (sigstore/cosign-installer) an error and checks that checksums.txt is
# signed by the release workflow at that tag; --require-provenance does the
# same for gh (preinstalled on GitHub-hosted runners) and the SLSA provenance.
# The installer is read from origin/main, as the trust step reads this script,
# and runs with PATH limited to /usr/bin, /bin and cosign's directory, so an
# earlier step that wrote GITHUB_PATH cannot shim its tools. Any failure
# exits non-zero and fails the job before aval is installed.
set -euo pipefail
: "${AVAL_VERSION:?}" "${AVAL_SHA256:?}"
tag="v$AVAL_VERSION"
dir="$RUNNER_TEMP/aval-$tag"
/usr/bin/mkdir -p "$dir"
/usr/bin/git show origin/main:scripts/get-aval.sh > "$dir/get-aval.sh"
cosign=$(command -v cosign) || { echo "::error::cosign is not installed"; exit 1; }
PATH="/usr/bin:/bin:${cosign%/*}" AVAL_INSTALL_DIR="$dir/bin" USE_SUDO=false \
  /bin/bash "$dir/get-aval.sh" --version "$tag" --sha256 "linux_amd64=$AVAL_SHA256" --require-verify --require-provenance
echo "$dir/bin" >> "$GITHUB_PATH"
