#!/bin/bash
# Usage: trust-from-base.sh (reads GITHUB_EVENT_NAME, RUNNER_TEMP, GITHUB_ENV)
# Sets AVAL_POLICY and AVAL_REGISTRY for the rest of the job (#134). On a
# pull request both come from main, never from the PR head or its base
# branch (a PR to another branch could control that), so a PR that adds its
# own identity to pr.yaml or points the registry elsewhere does not change
# what its CI trusts. On push (main) they come from the checkout, which is
# main. The registry is the one AVAL_REGISTRY value that ci.yaml declares;
# anything else fails the job. ci.yaml runs main's copy of this script with
# /bin/bash, and it calls every external tool by absolute path, so an earlier
# step that writes GITHUB_PATH cannot shim them. On pull_request the PR's own
# ci.yaml still runs, so a hostile PR can skip this script and make its own
# check green; required code-owner review protects main (docs/trust-model.md
# "Take the policy from a protected branch").
set -euo pipefail
: "${GITHUB_EVENT_NAME:?}" "${RUNNER_TEMP:?}" "${GITHUB_ENV:?}"
if [ "$GITHUB_EVENT_NAME" = pull_request ]; then
  /usr/bin/git fetch --no-tags --depth=1 origin +refs/heads/main:refs/remotes/origin/main
  src=origin/main policy=pr.yaml
else
  src=HEAD policy=main.yaml
fi
out="$RUNNER_TEMP/aval-policy.yaml"
/usr/bin/git show "$src:.github/aval/$policy" > "$out"
registry=$(/usr/bin/git show "$src:.github/workflows/ci.yaml" | /usr/bin/sed -n 's/^ *AVAL_REGISTRY: *//p' | /usr/bin/sort -u)
if ! [[ "$registry" =~ ^[a-z0-9][a-z0-9./:_-]*$ ]]; then
  echo "::error::$src:.github/workflows/ci.yaml must declare exactly one AVAL_REGISTRY, got: $registry"
  exit 1
fi
echo "AVAL_POLICY=$out" >> "$GITHUB_ENV"
echo "AVAL_REGISTRY=$registry" >> "$GITHUB_ENV"
echo "trust policy $src:.github/aval/$policy, registry $registry"
