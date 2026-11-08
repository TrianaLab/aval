#!/usr/bin/env bash
# Usage: trust-from-base.sh (reads GITHUB_EVENT_NAME, RUNNER_TEMP, GITHUB_ENV)
# Sets AVAL_POLICY and AVAL_REGISTRY for the rest of the job (#134). On a
# pull request both come from main, never from the PR head or its base
# branch (a PR to another branch could control that), so a PR that adds its
# own identity to pr.yaml or points the registry elsewhere does not change
# what its own CI trusts. On push (main) they come from the checkout, which
# is main. The registry is the one AVAL_REGISTRY value that ci.yaml declares;
# anything else fails the job. A PR can still edit ci.yaml, install.sh or
# this script for its own run: tests/architecture and enforced code-owner
# review guard those (docs/trust-model.md "Take the policy from a protected
# branch").
set -euo pipefail
: "${GITHUB_EVENT_NAME:?}" "${RUNNER_TEMP:?}" "${GITHUB_ENV:?}"
if [ "$GITHUB_EVENT_NAME" = pull_request ]; then
  git fetch --no-tags --depth=1 origin +refs/heads/main:refs/remotes/origin/main
  src=origin/main policy=pr.yaml
else
  src=HEAD policy=main.yaml
fi
out="$RUNNER_TEMP/aval-policy.yaml"
git show "$src:.github/aval/$policy" > "$out"
registry=$(git show "$src:.github/workflows/ci.yaml" | sed -n 's/^ *AVAL_REGISTRY: *//p' | sort -u)
if ! [[ "$registry" =~ ^[a-z0-9][a-z0-9./:_-]*$ ]]; then
  echo "::error::$src:.github/workflows/ci.yaml must declare exactly one AVAL_REGISTRY, got: $registry"
  exit 1
fi
echo "AVAL_POLICY=$out" >> "$GITHUB_ENV"
echo "AVAL_REGISTRY=$registry" >> "$GITHUB_ENV"
echo "trust policy $src:.github/aval/$policy, registry $registry"
