#!/usr/bin/env bash
# Tests scripts/deploy-docs.sh against a scratch copy of the repository whose
# origin is a local bare repo, so nothing leaves the machine.
#
#   scripts/test-deploy-docs.sh            # needs mike (docs/requirements.txt)
#
# It deploys main, a release v0.2.0, main again with a changed installer and a
# release v0.3.0, and checks after each step where the site root redirects,
# which versions carry `latest`, and that install.sh and install.ps1 at the
# gh-pages root match the scripts of the deployed commit. Set
# AVAL_TEST_KEEP=<dir> to keep the scratch copy (mike serve runs from it).
set -euo pipefail

src=$(cd "$(dirname "$0")/.." && pwd)
work=${AVAL_TEST_KEEP:-$(mktemp -d)}
[ -n "${AVAL_TEST_KEEP:-}" ] || trap 'rm -rf "$work"' EXIT
mkdir -p "$work"

pass() { printf 'ok   %s\n' "$1"; }
die() {
  printf 'FAIL %s\n' "$1" >&2
  exit 1
}

git init -q --bare "$work/origin.git"
mkdir "$work/repo"
(cd "$src" && git ls-files -z --cached --others --exclude-standard | xargs -0 tar -cf -) | tar -xf - -C "$work/repo"
cd "$work/repo"
git init -q -b main
git config user.name test
git config user.email test@example.invalid
git config commit.gpgsign false
git add -A
git commit -q -m init
git remote add origin "$work/origin.git"
git push -q origin main

pages() { git --git-dir="$work/origin.git" show "gh-pages:$1"; }

# check NAME DEFAULT: the root redirects to DEFAULT and the root installers
# match the scripts in the working tree.
check() {
  pages index.html | grep -q "url=$2/" || die "$1: root does not redirect to $2"
  [ "$(pages install.sh)" = "$(cat scripts/get-aval.sh)" ] || die "$1: install.sh is not scripts/get-aval.sh"
  [ "$(pages install.ps1)" = "$(cat scripts/install.ps1)" ] || die "$1: install.ps1 is not scripts/install.ps1"
  pass "$1: root redirects to $2, installers published"
}

deploy() { PUSH=true scripts/deploy-docs.sh "$@" > "$work/deploy.log" 2>&1 || { cat "$work/deploy.log" >&2; die "deploy $*"; }; }

deploy branch main
check "first main deploy" main

deploy tag v0.2.0
check "release v0.2.0" latest

echo "# changed" >> scripts/get-aval.sh
git commit -q -am "change the installer"
deploy branch main
check "main after a release" latest

deploy tag v0.3.0
check "release v0.3.0" latest

aliases=$(pages versions.json | python3 -c 'import json,sys; print(" ".join(v["version"] + ":" + ",".join(v["aliases"]) for v in json.load(sys.stdin)))')
[ "$aliases" = "main: 0.3.0:latest 0.2.0:" ] || die "versions.json: got '$aliases'"
pass "only 0.3.0 carries latest"
