#!/usr/bin/env bash
# Tests scripts/deploy-docs.sh against a scratch copy of the repository whose
# origin is a local bare repo, so nothing leaves the machine.
#
#   scripts/test-deploy-docs.sh            # needs mike (docs/requirements.txt) and Go
#
# It deploys main, a release v0.2.0, main again with a changed installer, a
# release v0.3.0 and a release v0.3.1 tagged on an older commit, and checks
# after each step where the site root redirects, which versions carry
# `latest`, that install.sh and install.ps1 at the gh-pages root are
# byte-equal to main's scripts, and that main and a release ship the
# "Try it" WebAssembly build. Then it publishes stats.json
# (scripts/publish-stats.sh) and checks that a docs deploy keeps it and that
# an invalid report never replaces it. Needs jq. Set
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
# are byte-equal to the scripts on main.
check() {
  pages index.html | grep -q "url=$2/" || die "$1: root does not redirect to $2"
  pages install.sh | cmp -s - <(git show main:scripts/get-aval.sh) || die "$1: install.sh is not main's scripts/get-aval.sh"
  pages install.ps1 | cmp -s - <(git show main:scripts/install.ps1) || die "$1: install.ps1 is not main's scripts/install.ps1"
  pass "$1: root redirects to $2, installers published"
}

# wasm VERSION: the version ships the "Try it" WebAssembly build.
wasm() {
  [ -n "$(git --git-dir="$work/origin.git" ls-tree "gh-pages:$1/assets/demo" aval.wasm)" ] || die "$1 has no assets/demo/aval.wasm"
  pass "$1 ships assets/demo/aval.wasm"
}

deploy() { PUSH=true scripts/deploy-docs.sh "$@" > "$work/deploy.log" 2>&1 || { cat "$work/deploy.log" >&2; die "deploy $*"; }; }

deploy branch main
check "first main deploy" main
wasm main

deploy tag v0.2.0
check "release v0.2.0" latest
wasm 0.2.0

echo "# changed" >> scripts/get-aval.sh
git commit -q -am "change the installer"
git push -q origin main
deploy branch main
check "main after a release" latest

deploy tag v0.3.0
check "release v0.3.0" latest

# A tag on an older commit must not roll the root installers back.
git checkout -q --detach HEAD~1
deploy tag v0.3.1
check "release v0.3.1 on an older commit" latest
git checkout -q main

aliases=$(pages versions.json | python3 -c 'import json,sys; print(" ".join(v["version"] + ":" + ",".join(v["aliases"]) for v in json.load(sys.stdin)))')
[ "$aliases" = "main: 0.3.1:latest 0.3.0: 0.2.0:" ] || die "versions.json: got '$aliases'"
pass "only 0.3.1 carries latest"

# stats.json (#97): a valid stats report is published at the gh-pages root
# with generatedAt, survives a docs deploy, and anything else leaves it as
# it was.
golden=pkg/evidence/testdata/stats-report-v1.golden.json
publish() { PUSH=true scripts/publish-stats.sh "$1" > "$work/stats.log" 2>&1; }
publish "$golden" || { cat "$work/stats.log" >&2; die "publish the golden stats report"; }
pages stats.json | jq -e --slurpfile want "$golden" 'del(.generatedAt) == $want[0] and (.generatedAt | fromdateiso8601) > 0' >/dev/null ||
  die "stats.json is not the report plus generatedAt"
pass "stats.json published with generatedAt"
before=$(pages stats.json)

deploy branch main
[ "$(pages stats.json)" = "$before" ] || die "a docs deploy changed stats.json"
pass "a docs deploy keeps stats.json"

echo '{"version":"aval.trianalab.io/stats-report/v2","total":{"runs":1},"tests":[],"repositories":[]}' > "$work/v2.json"
jq 'del(.repositories)' "$golden" > "$work/by-test.json"
echo 'not json' > "$work/garbage.json"
for bad in v2 by-test garbage missing; do
  ! publish "$work/$bad.json" || die "$bad: published"
  [ "$(pages stats.json)" = "$before" ] || die "$bad: stats.json changed"
done
[ "$(git worktree list | wc -l)" -eq 1 ] || die "publish-stats left a worktree behind"
pass "an invalid or missing report keeps the previous stats.json"
