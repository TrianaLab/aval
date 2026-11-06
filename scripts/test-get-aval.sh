#!/usr/bin/env bash
# Tests scripts/get-aval.sh against Aval's real GitHub releases.
#
#   scripts/test-get-aval.sh            # needs network, cosign, gh and python3
#
# Run it with the bash you want to test (CI runs it with macOS's /bin/bash
# 3.2): the installer runs under the same interpreter ($BASH).
#
# The happy paths install the latest release and a pinned older one (piped on
# stdin, as `curl | bash` does). The tamper cases serve a local mirror of a
# real release through AVAL_DOWNLOAD_URL, with one file altered each time,
# and require the install to fail for the expected reason and install nothing.
set -euo pipefail

VERSION="${AVAL_TEST_VERSION:-v0.2.0}"      # release mirrored for the tamper cases
OLD_VERSION="${AVAL_TEST_OLD_VERSION:-v0.1.0}" # pinned install, and a foreign provenance
PORT="${AVAL_TEST_PORT:-8765}"

installer="$(cd "$(dirname "$0")" && pwd)/get-aval.sh"
work=$(mktemp -d)
server=""
cleanup() {
  if [ -n "$server" ]; then kill "$server" 2>/dev/null || true; fi
  rm -rf "$work"
  return 0
}
trap cleanup EXIT

for tool in cosign gh python3; do
  command -v "$tool" >/dev/null 2>&1 || { echo "FAIL: $tool is required" >&2; exit 1; }
done

pass() { printf 'ok   %s\n' "$1"; }
die() {
  printf 'FAIL %s\n' "$1" >&2
  exit 1
}

# run_installer NAME [VAR=value...]: runs the installer with its install dir under
# $work/NAME and the given environment; leaves the output in $work/NAME.log.
run_installer() {
  local name="$1"
  shift
  env USE_SUDO=false AVAL_INSTALL_DIR="$work/$name" "$@" "$BASH" "$installer" >"$work/$name.log" 2>&1
}

# want_fail NAME MESSAGE [VAR=value...]: the install fails, says MESSAGE and
# leaves no binary behind.
want_fail() {
  local name="$1" msg="$2"
  shift 2
  if run_installer "$name" "$@"; then cat "$work/$name.log"; die "$name: install succeeded"; fi
  grep -qF "$msg" "$work/$name.log" || { cat "$work/$name.log"; die "$name: output lacks '$msg'"; }
  [ ! -e "$work/$name/aval" ] || die "$name: aval was installed"
  pass "$name"
}

# Latest release, with every check.
run_installer latest || { cat "$work/latest.log"; die "latest: install failed"; }
for line in "signature ok" "sha256 ok" "provenance ok" "aval version "; do
  grep -qF "$line" "$work/latest.log" || { cat "$work/latest.log"; die "latest: output lacks '$line'"; }
done
"$work/latest/aval" --version >/dev/null
pass "latest"

# Pinned older release, script on stdin, without the v prefix.
env USE_SUDO=false AVAL_INSTALL_DIR="$work/pinned" DESIRED_VERSION="${OLD_VERSION#v}" "$BASH" <"$installer" >"$work/pinned.log" 2>&1 \
  || { cat "$work/pinned.log"; die "pinned: install failed"; }
grep -qF "aval version ${OLD_VERSION#v}" "$work/pinned.log" || { cat "$work/pinned.log"; die "pinned: wrong version"; }
pass "pinned"

want_fail bad-version "not a release version" DESIRED_VERSION='1.0;rm'
want_fail no-release "not found" DESIRED_VERSION=v0.0.0

# Local mirror of $VERSION, one directory per case: <case>/<tag>/<file>.
case "$(uname -s)" in Darwin) os=darwin ;; *) os=linux ;; esac
case "$(uname -m)" in aarch64 | arm64) arch=arm64 ;; *) arch=amd64 ;; esac
archive="aval_${VERSION#v}_${os}_${arch}.tar.gz"
real="$work/real"
mkdir -p "$real"
for f in "$archive" checksums.txt checksums.txt.sigstore.json provenance.sigstore.json; do
  curl -fsSL --retry 3 -o "$real/$f" "https://github.com/TrianaLab/aval/releases/download/$VERSION/$f"
done
curl -fsSL --retry 3 -o "$real/old-provenance.sigstore.json" \
  "https://github.com/TrianaLab/aval/releases/download/$OLD_VERSION/provenance.sigstore.json"

mirror() {
  mkdir -p "$work/mirror/$1/$VERSION"
  cp "$real/$archive" "$real/checksums.txt" "$real/checksums.txt.sigstore.json" "$real/provenance.sigstore.json" \
    "$work/mirror/$1/$VERSION/"
  printf '%s\n' "$work/mirror/$1/$VERSION"
}

# Genuine files served from the mirror install fine: the mirror itself is not
# what the tamper cases detect.
mirror genuine >/dev/null
d=$(mirror tampered-archive)
printf 'x' >>"$d/$archive"
d=$(mirror tampered-checksums)
tampered_sha=$( (sha256sum "$work/mirror/tampered-archive/$VERSION/$archive" 2>/dev/null || shasum -a 256 "$work/mirror/tampered-archive/$VERSION/$archive") | awk '{print $1}')
cp "$work/mirror/tampered-archive/$VERSION/$archive" "$d/$archive"
awk -v f="$archive" -v s="$tampered_sha" '$2 == f {$1 = s; print $1 "  " $2; next} {print}' "$real/checksums.txt" >"$d/checksums.txt"
d=$(mirror unlisted)
grep -vF "  $archive" "$real/checksums.txt" >"$d/checksums.txt"
d=$(mirror no-checksums)
rm "$d/checksums.txt"
d=$(mirror foreign-provenance)
cp "$real/old-provenance.sigstore.json" "$d/provenance.sigstore.json"

python3 -m http.server "$PORT" --bind 127.0.0.1 --directory "$work/mirror" >/dev/null 2>&1 &
server=$!
for _ in $(seq 1 50); do
  curl -fsS -o /dev/null "http://127.0.0.1:$PORT/genuine/$VERSION/checksums.txt" 2>/dev/null && break
  sleep 0.2
done
url="http://127.0.0.1:$PORT"

run_installer genuine DESIRED_VERSION="$VERSION" AVAL_DOWNLOAD_URL="$url/genuine" \
  || { cat "$work/genuine.log"; die "genuine: mirror install failed"; }
pass "genuine"

want_fail tampered-archive "checksum mismatch" DESIRED_VERSION="$VERSION" AVAL_DOWNLOAD_URL="$url/tampered-archive"
# --no-verify never skips the sha256.
want_fail tampered-archive-no-verify "checksum mismatch" DESIRED_VERSION="$VERSION" AVAL_DOWNLOAD_URL="$url/tampered-archive" AVAL_NO_VERIFY=1
grep -qF "warning: --no-verify" "$work/tampered-archive-no-verify.log" || die "no-verify: no warning printed"
# checksums.txt rewritten to match the tampered archive: only the signature catches it.
want_fail tampered-checksums "is not signed by" DESIRED_VERSION="$VERSION" AVAL_DOWNLOAD_URL="$url/tampered-checksums"
want_fail unlisted "does not list" DESIRED_VERSION="$VERSION" AVAL_DOWNLOAD_URL="$url/unlisted" AVAL_NO_VERIFY=1
want_fail no-checksums "not found" DESIRED_VERSION="$VERSION" AVAL_DOWNLOAD_URL="$url/no-checksums"
# Another release's provenance does not cover this archive: only gh catches it.
want_fail foreign-provenance "does not match the provenance" DESIRED_VERSION="$VERSION" AVAL_DOWNLOAD_URL="$url/foreign-provenance"

echo "all installer cases passed"
