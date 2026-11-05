#!/usr/bin/env bash
# Usage: install.sh (reads AVAL_VERSION and AVAL_SHA256, pinned in ci.yaml)
# Installs the pinned aval release (linux/amd64) on PATH, verified before use:
# the archive must have the sha256 pinned in the workflow, so a re-tagged
# release cannot change what runs; checksums.txt must carry a keyless
# signature from the release workflow at that tag; and it must list the
# archive with the same sha256. Needs cosign on PATH. Any failure exits
# non-zero and fails the job before aval is installed.
set -euo pipefail
: "${AVAL_VERSION:?}" "${AVAL_SHA256:?}"
tag="v$AVAL_VERSION"
archive="aval_${AVAL_VERSION}_linux_amd64.tar.gz"
dir="$RUNNER_TEMP/aval-$tag"
mkdir -p "$dir/bin"
cd "$dir"
for f in "$archive" checksums.txt checksums.txt.sigstore.json; do
  curl -fsSL --retry 5 --retry-all-errors -o "$f" "https://github.com/TrianaLab/aval/releases/download/$tag/$f"
done
echo "$AVAL_SHA256  $archive" | sha256sum -c -
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity "https://github.com/TrianaLab/aval/.github/workflows/release.yaml@refs/tags/$tag"
grep -qxF "$AVAL_SHA256  $archive" checksums.txt || { echo "::error::checksums.txt does not list $archive with the pinned sha256"; exit 1; }
tar -xzf "$archive" -C bin aval
echo "$dir/bin" >> "$GITHUB_PATH"
"$dir/bin/aval" --version
