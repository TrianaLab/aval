#!/usr/bin/env bash
# One-line installer for Aval (Linux and macOS).
#
#   curl -fsSL https://raw.githubusercontent.com/TrianaLab/aval/main/scripts/get-aval.sh | bash
#
# Windows uses scripts/install.ps1. `docs/install.sh` is a symlink to this
# file, so the docs site (#81, not merged yet) can publish these same bytes;
# a copy would drift from the original by the next release.
#
# What it does, in order: detect OS and architecture, resolve the release
# (latest, or DESIRED_VERSION), download the archive and checksums.txt,
# verify them, and install `aval` into AVAL_INSTALL_DIR.
#
# Aval exists to reuse results only after verification, so its installer
# does not install anything it has not checked:
#
#   * checksums.txt must exist and list the archive, and the archive must have
#     that sha256. Any other case aborts. This check always runs, also with
#     --no-verify.
#   * If cosign is on PATH, checksums.txt must carry a keyless signature from
#     TrianaLab/aval's release workflow at this tag. That proves who published
#     the checksums, not only that the download is intact.
#   * If gh is on PATH, the archive must match the SLSA provenance that the
#     same workflow attested. The bundle comes from the release itself, so this
#     needs no GitHub login and no API call.
#   * Without cosign or gh, the script says which checks did not run and how
#     to run them later. --no-verify (or AVAL_NO_VERIFY=1) skips the cosign and
#     gh checks even when the tools are present, and prints a warning.
#
# Environment:
#   DESIRED_VERSION    release to install, e.g. v0.2.0 (default: latest)
#   AVAL_INSTALL_DIR   install directory (default: /usr/local/bin)
#   USE_SUDO           "false" never uses sudo (default: sudo only when the
#                      directory is not writable)
#   AVAL_NO_VERIFY     "1" is the same as --no-verify
#   AVAL_DOWNLOAD_URL  release download base, for a mirror (default: GitHub
#                      Releases). The signer identity stays pinned to
#                      TrianaLab/aval, so a mirror cannot serve another build
#                      when cosign or gh is present. The CI tamper test uses it.
set -euo pipefail

REPO="TrianaLab/aval"
# The identity GoReleaser's cosign step and attest-build-provenance sign with
# (.github/workflows/release.yaml); the tag is appended once it is known.
SIGNER="https://github.com/$REPO/.github/workflows/release.yaml@refs/tags"
ISSUER="https://token.actions.githubusercontent.com"

: "${AVAL_INSTALL_DIR:=/usr/local/bin}"
: "${USE_SUDO:=true}"
: "${AVAL_DOWNLOAD_URL:=https://github.com/$REPO/releases/download}"
DESIRED_VERSION="${DESIRED_VERSION:-}"
NO_VERIFY="false"
if [ "${AVAL_NO_VERIFY:-}" = "1" ]; then NO_VERIFY="true"; fi

TMP=""

say() { printf '%s\n' "$*"; }
fail() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

usage() {
  say "Usage: get-aval.sh [--version <version>] [--no-sudo] [--no-verify] [--help]"
  say "  --version, -v  release to install, e.g. v0.2.0 (default: latest)"
  say "  --no-sudo      never use sudo"
  say "  --no-verify    skip the cosign and gh checks (the sha256 is always checked)"
  say "See the header of this script for the environment variables."
}

# The status of an EXIT trap's last command becomes the script's status, so
# this ends on a command that succeeds; otherwise `--help` would exit 1.
cleanup() {
  if [ -n "$TMP" ]; then rm -rf "$TMP"; fi
  return 0
}
trap cleanup EXIT

while [ $# -gt 0 ]; do
  case "$1" in
    --version | -v)
      [ -n "${2:-}" ] || fail "--version needs a value"
      DESIRED_VERSION="$2"
      shift
      ;;
    --no-sudo) USE_SUDO="false" ;;
    --no-verify) NO_VERIFY="true" ;;
    --help | -h)
      usage
      exit 0
      ;;
    *)
      usage >&2
      fail "unknown option: $1"
      ;;
  esac
  shift
done

if command -v curl >/dev/null 2>&1; then
  HAS_CURL=true
elif command -v wget >/dev/null 2>&1; then
  HAS_CURL=false
else
  fail "curl or wget is required"
fi

# download URL FILE. Fails (non-zero) on any HTTP error, so a 404 never
# leaves an error page behind that a later step could mistake for a file.
download() {
  if [ "$HAS_CURL" = true ]; then
    curl -fsSL --retry 3 -o "$2" "$1"
  else
    wget -q --tries=3 -O "$2" "$1"
  fi
}

# The latest tag comes from the redirect of /releases/latest to
# /releases/tag/<tag>, not from api.github.com: the web redirect has no
# anonymous rate limit (the API allows 60 requests per hour per IP, which
# shared CI runners exhaust), so no token is needed or handled here.
latest_tag() {
  local url="https://github.com/$REPO/releases/latest" final
  if [ "$HAS_CURL" = true ]; then
    final=$(curl -fsSLI --retry 3 -o /dev/null -w '%{url_effective}' "$url") || return 1
  else
    # wget exits non-zero on a redirect it is told not to follow, hence || true.
    final=$(wget -S --spider --max-redirect=0 "$url" 2>&1 | awk 'tolower($1) == "location:" {print $2}' | tail -n 1 | tr -d '\r' || true)
  fi
  printf '%s\n' "${final##*/}"
}

sha256_of() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  else
    # No way to check the download means no install (fail closed).
    fail "sha256sum or shasum is required to verify the download"
  fi
}

case "$(uname -s)" in
  Linux) OS=linux ;;
  Darwin) OS=darwin ;;
  MINGW* | MSYS* | CYGWIN*) fail "on Windows, use install.ps1: irm https://raw.githubusercontent.com/$REPO/main/scripts/install.ps1 | iex" ;;
  *) fail "unsupported OS: $(uname -s)" ;;
esac
case "$(uname -m)" in
  x86_64 | amd64) ARCH=amd64 ;;
  aarch64 | arm64) ARCH=arm64 ;;
  *) fail "unsupported architecture: $(uname -m)" ;;
esac

if [ -n "$DESIRED_VERSION" ]; then
  TAG="v${DESIRED_VERSION#v}"
else
  TAG=$(latest_tag) || fail "could not resolve the latest release of $REPO"
fi
# The tag goes into URLs and into the signer identity, so it must look like a
# release tag and nothing else.
if ! printf '%s\n' "$TAG" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'; then
  fail "not a release version: '$TAG'"
fi
VERSION="${TAG#v}"
ARCHIVE="aval_${VERSION}_${OS}_${ARCH}.tar.gz"
BASE="$AVAL_DOWNLOAD_URL/$TAG"

say "Installing aval $TAG ($OS/$ARCH) into $AVAL_INSTALL_DIR"
TMP=$(mktemp -d)
download "$BASE/checksums.txt" "$TMP/checksums.txt" || fail "release $TAG not found (no checksums.txt at $BASE)"
download "$BASE/$ARCHIVE" "$TMP/$ARCHIVE" || fail "release $TAG has no $ARCHIVE"

if [ "$NO_VERIFY" = "true" ]; then
  say "warning: --no-verify: the signature and provenance are NOT checked, only the sha256" >&2
fi

# 1. Who published checksums.txt. Checked before the sha256 is trusted,
# because a checksum file served next to the archive proves nothing alone.
if [ "$NO_VERIFY" = "false" ]; then
  if command -v cosign >/dev/null 2>&1; then
    download "$BASE/checksums.txt.sigstore.json" "$TMP/checksums.txt.sigstore.json" \
      || fail "release $TAG has no checksums.txt.sigstore.json"
    cosign verify-blob "$TMP/checksums.txt" --bundle "$TMP/checksums.txt.sigstore.json" \
      --certificate-oidc-issuer "$ISSUER" --certificate-identity "$SIGNER/$TAG" >/dev/null 2>&1 \
      || fail "checksums.txt is not signed by $SIGNER/$TAG (cosign verify-blob failed)"
    say "  signature ok (cosign: checksums.txt signed by the release workflow)"
  else
    say "  signature not checked: cosign is not installed" >&2
  fi
fi

# 2. The archive matches checksums.txt. Exact match on the file name column:
# a substring match would let aval_X_linux_amd64.tar.gz.sbom.json answer for
# the archive.
expected=$(awk -v f="$ARCHIVE" '$2 == f {print $1}' "$TMP/checksums.txt")
[ -n "$expected" ] || fail "checksums.txt does not list $ARCHIVE"
actual=$(sha256_of "$TMP/$ARCHIVE")
if [ "$expected" != "$actual" ]; then
  fail "checksum mismatch for $ARCHIVE: expected $expected, got $actual"
fi
say "  sha256 ok"

# 3. Who built the archive: the SLSA provenance from the release workflow.
# `gh attestation` needs gh 2.49 or later; an older gh counts as absent.
if [ "$NO_VERIFY" = "false" ]; then
  if command -v gh >/dev/null 2>&1 && gh attestation verify --help >/dev/null 2>&1; then
    download "$BASE/provenance.sigstore.json" "$TMP/provenance.sigstore.json" \
      || fail "release $TAG has no provenance.sigstore.json"
    gh attestation verify "$TMP/$ARCHIVE" --bundle "$TMP/provenance.sigstore.json" \
      --repo "$REPO" --cert-identity "$SIGNER/$TAG" --cert-oidc-issuer "$ISSUER" >/dev/null 2>&1 \
      || fail "$ARCHIVE does not match the provenance of $SIGNER/$TAG (gh attestation verify failed)"
    say "  provenance ok (gh: built by the release workflow)"
  else
    say "  provenance not checked: gh (2.49 or later) is not installed" >&2
  fi
fi

tar -xzf "$TMP/$ARCHIVE" -C "$TMP" aval

# sudo only when the directory cannot be written as the current user, and
# never with USE_SUDO=false (the install then fails with the real error).
SUDO=""
if [ "$USE_SUDO" = "true" ] && [ "$(id -u)" -ne 0 ]; then
  if { [ -d "$AVAL_INSTALL_DIR" ] && [ ! -w "$AVAL_INSTALL_DIR" ]; } \
    || { [ ! -d "$AVAL_INSTALL_DIR" ] && [ ! -w "$(dirname "$AVAL_INSTALL_DIR")" ]; }; then
    SUDO="sudo"
    say "  $AVAL_INSTALL_DIR is not writable, using sudo"
  fi
fi
# $SUDO is unquoted on purpose: empty means "run directly".
$SUDO mkdir -p "$AVAL_INSTALL_DIR"
$SUDO install -m 0755 "$TMP/aval" "$AVAL_INSTALL_DIR/aval"

say "aval installed to $AVAL_INSTALL_DIR/aval"
"$AVAL_INSTALL_DIR/aval" --version
case ":$PATH:" in
  *":$AVAL_INSTALL_DIR:"*) ;;
  *) say "note: $AVAL_INSTALL_DIR is not on PATH; add it to run aval by name" ;;
esac
