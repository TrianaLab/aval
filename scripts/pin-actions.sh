#!/bin/bash
# Usage: pin-actions.sh <tag> <checksums.txt> <action.yml>...
# Points the install step of each action.yml at release <tag>: rewrites
# DESIRED_VERSION and the per-platform sha256 in AVAL_SHA256 from that
# release's checksums.txt (#213). Fails, changing nothing, when the tag is
# malformed or checksums.txt does not list exactly one sha256 per platform.
# The pin-actions job of .github/workflows/release.yaml runs it after each
# release; tests/release checks the result (TestActionPins*).
set -euo pipefail
[ $# -ge 3 ] || { echo "usage: pin-actions.sh <tag> <checksums.txt> <action.yml>..." >&2; exit 2; }
tag=$1 sums=$2
shift 2
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "bad tag: $tag" >&2; exit 1; }
edits=(-e "s/^\( *DESIRED_VERSION: \)v[^ ]*\$/\1$tag/")
for p in linux_amd64 linux_arm64 darwin_amd64 darwin_arm64; do
  archive="aval_${tag#v}_$p.tar.gz"
  sum=$(awk -v f="$archive" '$2 == f {print $1}' "$sums")
  [[ "$sum" =~ ^[0-9a-f]{64}$ ]] || { echo "$sums does not list exactly one sha256 for $archive" >&2; exit 1; }
  edits+=(-e "s/^\( *\)$p=[0-9a-f]\{64\}\$/\1$p=$sum/")
done
for f in "$@"; do
  sed "${edits[@]}" "$f" > "$f.tmp"
  mv "$f.tmp" "$f"
done
