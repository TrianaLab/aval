#!/bin/sh
# Estimates how often Aval would have reused a result over past commits,
# from the fingerprints alone. Run it from the repository root:
#
#   scripts/estimate-reuse.sh path/to/aval.yaml [git rev-list args]
#
# The rev-list args default to the last 50 commits of HEAD. Each commit is
# checked out in a scratch git worktree and fingerprinted with
# `aval run --fingerprint-only`. A fingerprint seen on an earlier commit is a
# hit. A fingerprint error or an unacknowledged mutable reference (a
# `mutable:` line on stderr without `(allowed: ...)`) is a miss, and that
# commit publishes nothing, so it never makes a later commit a hit. A
# non-deterministic contract is always a miss (invariant 7).
# The result is an upper bound: it ignores branches, failed tests and trust.
# AVAL overrides the aval binary. tests/acceptance/estimate_test.go tests it.
set -eu

contract=${1:?usage: estimate-reuse.sh path/to/aval.yaml [git rev-list args]}
case $contract in /*) echo "estimate-reuse.sh: give the contract path relative to the repository root" >&2; exit 2;; esac
shift
[ $# -gt 0 ] || set -- --max-count=50 HEAD
aval=${AVAL:-aval}
case $aval in */*) aval=$(cd "$(dirname "$aval")" && pwd)/${aval##*/};; esac
cd "$(git rev-parse --show-toplevel)"

tmp=$(mktemp -d)
wt=$tmp/wt
trap 'git worktree remove --force "$wt" >/dev/null 2>&1 || true; rm -rf "$tmp"' EXIT
git worktree add --quiet --detach "$wt" HEAD
: >"$tmp/seen"

total=0 hits=0
for c in $(git rev-list --reverse "$@"); do
	total=$((total + 1))
	git -C "$wt" checkout --quiet --detach --force "$c"
	if grep -q "^cacheability:[ \"']*non-deterministic" "$wt/$contract" 2>/dev/null; then
		echo "$c miss non-deterministic: never reusable"
		continue
	fi
	if ! fp=$(cd "$wt" && "$aval" run --fingerprint-only -f "$contract" 2>"$tmp/err"); then
		echo "$c miss $(tail -n 1 "$tmp/err")"
	elif grep '^mutable: ' "$tmp/err" | grep -qv '(allowed: '; then
		echo "$c miss unacknowledged mutable reference"
	elif grep -qxF "$fp" "$tmp/seen"; then
		hits=$((hits + 1))
		echo "$c hit $fp"
	else
		echo "$fp" >>"$tmp/seen"
		echo "$c miss $fp"
	fi
done
[ "$total" -gt 0 ] || { echo "no commits" >&2; exit 1; }
echo "$total commits, $hits would reuse a result ($((hits * 100 / total))%, an upper bound)"
