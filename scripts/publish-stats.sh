#!/usr/bin/env bash
# Publishes the site's live statistics (#97) to the gh-pages root as
# stats.json (.github/workflows/docs.yaml), without rebuilding any page.
#
#   scripts/publish-stats.sh report.json   # from `aval stats -o json --by repository`
#
# The report must be a stats report (aval.trianalab.io/stats-report/v1);
# stats.json is that report plus `generatedAt`, the time of publishing, which
# the site uses for "updated N minutes ago" and to spot a stale file. On any
# error stats.json is left as it was and the script exits 1. PUSH=true pushes
# gh-pages to origin. Needs jq. scripts/test-deploy-docs.sh tests it.
set -euo pipefail

report=${1:?usage: publish-stats.sh <stats report json>}
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"

new=$(mktemp)
pages=$(mktemp -d)
rmdir "$pages"
trap 'rm -f "$new"; git worktree remove --force "$pages" 2>/dev/null || true' EXIT

# Run records are unsigned, so the report is untrusted: only a well-formed
# v1 report is published.
jq -e --arg t "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '
  select(.version == "aval.trianalab.io/stats-report/v1"
    and (.total.runs | type) == "number"
    and (.tests | type) == "array"
    and (.repositories | type) == "array")
  | . + {generatedAt: $t}' "$report" > "$new" ||
  { echo "publish-stats: $report is not a stats report v1 with repositories; stats.json not changed" >&2; exit 1; }

git fetch -q origin gh-pages:gh-pages 2>/dev/null || true
git rev-parse -q --verify gh-pages >/dev/null ||
  { echo "publish-stats: no gh-pages branch; deploy the docs first" >&2; exit 1; }
git worktree add -q "$pages" gh-pages
cp "$new" "$pages/stats.json"
git -C "$pages" add stats.json
git -C "$pages" commit -q -m "Publish stats.json"

if [ "${PUSH:-false}" = "true" ]; then
  git push -q origin gh-pages
fi
