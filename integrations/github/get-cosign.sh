#!/bin/bash
# Installs the cosign release COSIGN_VERSION into
# $RUNNER_TEMP/aval-cosign-$COSIGN_VERSION for scripts/get-aval.sh only, and
# writes that dir to the step output "dir" (#248). Never writes GITHUB_PATH or
# GITHUB_ENV: the test and the rest of the consumer's job keep their own PATH.
# The binary must have the sha256 pinned for this runner in COSIGN_SHA256
# (cosign-<os>-<arch>=<hex>, separated by spaces). A mismatch or an unpinned
# platform fails and installs nothing. COSIGN_BASE_URL is where the release
# assets live (the action fixes it; tests point it at a local server).
# Tools by absolute path, so an earlier step that wrote GITHUB_PATH cannot
# shim them (tests/architecture TestTrustScriptsCallToolsByAbsolutePath).
set -euo pipefail
set -f

fail() { echo "get-cosign: $*" >&2; exit 1; }

case "$(/usr/bin/uname -s)" in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "unsupported OS: $(/usr/bin/uname -s)" ;;
esac
case "$(/usr/bin/uname -m)" in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "unsupported architecture: $(/usr/bin/uname -m)" ;;
esac
asset="cosign-$os-$arch"

[[ "$COSIGN_VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || fail "bad COSIGN_VERSION: $COSIGN_VERSION"
pin=""
for entry in $COSIGN_SHA256; do
  [ "${entry%%=*}" = "$asset" ] || continue
  [ -z "$pin" ] || fail "COSIGN_SHA256 pins $asset twice"
  pin=${entry#*=}
done
[[ "$pin" =~ ^[0-9a-f]{64}$ ]] || fail "COSIGN_SHA256 has no sha256 for $asset"

tmp=$(/usr/bin/mktemp -d "$RUNNER_TEMP/aval-cosign.XXXXXX")
trap '/bin/rm -rf "$tmp"' EXIT
/usr/bin/curl -fsSL --retry 3 -o "$tmp/cosign" "$COSIGN_BASE_URL/$COSIGN_VERSION/$asset"
if [ -x /usr/bin/sha256sum ]; then
  got=$(/usr/bin/sha256sum "$tmp/cosign")
else
  got=$(/usr/bin/shasum -a 256 "$tmp/cosign")
fi
got=${got%% *}
[ "$got" = "$pin" ] || fail "sha256 mismatch for $asset $COSIGN_VERSION: pinned $pin, got $got"

dir="$RUNNER_TEMP/aval-cosign-$COSIGN_VERSION"
/bin/mkdir -p "$dir"
/bin/chmod 0755 "$tmp/cosign"
/bin/mv -f "$tmp/cosign" "$dir/cosign"
echo "dir=$dir" >> "$GITHUB_OUTPUT"
