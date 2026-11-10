#!/bin/bash
# Steps 3, 4, 6 and 7 of the aval publish action (#212). Reads INPUT_FILE,
# INPUT_POLICY, INPUT_STATS, AVAL (absolute path of the installed binary),
# ARTIFACTS (the directory the aval-* artifacts were downloaded into),
# REGISTRY_USERNAME, REGISTRY_PASSWORD, DEFAULT_BRANCH, GH_TOKEN and the
# runner's GITHUB_*, RUNNER_TEMP and ACTIONS_ID_TOKEN_REQUEST_* variables.
#
# The policy, and the registry inside it, come from the default branch
# through the contents API, as in the run action. Each artifact aval-<name>
# is pushed with --test <name>, so it can only publish the test it is named
# after, with a fresh OIDC token: the signer is the calling workflow's job.
# Every artifact is tried; the step exits with the worst code, and 5 (a
# security event) is never downgraded. Tools are called by absolute path, so
# an earlier step that writes GITHUB_PATH cannot shim them.
set -euo pipefail
: "${INPUT_FILE:?}" "${INPUT_POLICY:?}" "${AVAL:?}" "${ARTIFACTS:?}" "${GH_TOKEN:?}" "${DEFAULT_BRANCH:?}"
: "${GITHUB_API_URL:?}" "${GITHUB_REPOSITORY:?}" "${RUNNER_TEMP:?}" "${GITHUB_STEP_SUMMARY:?}"
: "${ACTIONS_ID_TOKEN_REQUEST_URL:?}" "${ACTIONS_ID_TOKEN_REQUEST_TOKEN:?}"
unset AVAL_REGISTRY AVAL_POLICY SIGSTORE_ID_TOKEN

if ! [[ "$INPUT_POLICY" =~ ^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$ ]] || [[ "/$INPUT_POLICY/" == */../* ]]; then
  echo "::error title=aval publish::policy: \"$INPUT_POLICY\" must be a relative path in the repository"
  exit 2
fi

# Step 3: the policy from the default branch. Without it there is no
# registry to publish to.
policy="$RUNNER_TEMP/aval-publish-policy.yaml"
ref=$(/usr/bin/jq -rn --arg v "$DEFAULT_BRANCH" '$v | @uri')
# The token goes through stdin, not the command line.
status=$(printf 'header = "Authorization: Bearer %s"\n' "$GH_TOKEN" |
  /usr/bin/curl -sS --retry 3 -K - -o "$policy" -w '%{http_code}' \
    -H "Accept: application/vnd.github.raw+json" -H "X-GitHub-Api-Version: 2022-11-28" \
    "$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/contents/$INPUT_POLICY?ref=$ref") || status=000
if [ "$status" != 200 ]; then
  echo "::error title=aval publish::no trust policy at $INPUT_POLICY in the default branch ($DEFAULT_BRANCH, HTTP $status): it names the registry to publish to"
  exit 1
fi
# ponytail: line-based read of the top-level registry: key in block-style
# YAML, as in the run action's policy warning; aval validates the value.
registry=$(/usr/bin/awk '
  /^registry:/ {
    s = $0; sub(/^registry:[ \t]*/, "", s)
    if (s ~ /^".*"$/ || s ~ /^'"'"'.*'"'"'$/) s = substr(s, 2, length(s) - 2)
    else sub(/[ \t]+#.*$/, "", s)
    print s; exit
  }' "$policy")
if ! [[ "$registry" =~ ^[A-Za-z0-9.-]+(:[0-9]+)?/[a-z0-9._/-]+$ ]]; then
  echo "::error title=aval publish::the trust policy at $INPUT_POLICY has no valid registry field: \"$registry\""
  exit 1
fi
echo "trust policy: $INPUT_POLICY from the default branch ($DEFAULT_BRANCH), registry $registry"

# Step 4: credentials for the registry host only, in a credential store of
# this step, never the job's.
export DOCKER_CONFIG="$RUNNER_TEMP/aval-publish-docker"
trap '/bin/rm -rf "$DOCKER_CONFIG"' EXIT
/bin/mkdir -p "$DOCKER_CONFIG"
if [ -n "${REGISTRY_USERNAME:-}" ] && [ -n "${REGISTRY_PASSWORD:-}" ]; then
  printf '%s:%s' "$REGISTRY_USERNAME" "$REGISTRY_PASSWORD" | /usr/bin/base64 | /usr/bin/tr -d '\n' |
    /usr/bin/jq -Rc --arg host "${registry%%/*}" '{auths: {($host): {auth: .}}}' > "$DOCKER_CONFIG/config.json"
fi

# Step 6: one push per artifact, each with its own token.
worst=0 pushed=0
echo "| test | aval push |" >> "$GITHUB_STEP_SUMMARY"
echo "|---|---|" >> "$GITHUB_STEP_SUMMARY"
for dir in "$ARTIFACTS"/aval-*/; do
  [ -d "$dir" ] || continue
  name=${dir%/}
  name=${name##*/aval-}
  code=0
  # metadata.name's syntax (internal/contract).
  if ! [[ "$name" =~ ^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$ ]]; then
    echo "::error title=aval publish::artifact aval-$name: \"$name\" is not a valid metadata.name"
    code=2
  elif ! token=$(printf 'header = "Authorization: Bearer %s"\n' "$ACTIONS_ID_TOKEN_REQUEST_TOKEN" |
    /usr/bin/curl -fsS --retry 3 -K - "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=sigstore" | /usr/bin/jq -er '.value'); then
    echo "::error title=aval publish::$name: could not get an OIDC token for Sigstore"
    code=1
  else
    pushed=$((pushed + 1))
    SIGSTORE_ID_TOKEN="$token" "$AVAL" push -f "$INPUT_FILE" --test "$name" --registry "$registry" \
      --report-file "${dir}report.json" --result-file "${dir}result.json" || code=$?
  fi
  echo "| $name | exit $code |" >> "$GITHUB_STEP_SUMMARY"
  if [ "$code" -eq 5 ]; then
    echo "::error title=aval SECURITY::$name: the report records a verification failure; nothing was published for it. Investigate before publishing"
  elif [ "$code" -ne 0 ]; then
    echo "::error title=aval publish::$name: aval push exited $code; nothing was published for it"
  fi
  if [ "$code" -eq 5 ] || { [ "$worst" -ne 5 ] && [ "$code" -gt "$worst" ]; }; then
    worst=$code
  fi

  # Step 7: best effort, never fails the step.
  if [ "${INPUT_STATS:-false}" = true ] && [ "$code" -eq 0 ] && [ -f "${dir}record.json" ]; then
    "$AVAL" stats publish --registry "$registry" --source-repo "$GITHUB_REPOSITORY" "${dir}record.json" ||
      echo "::warning title=aval publish::$name: could not publish the run record"
  fi
done
if [ "$pushed" -eq 0 ] && [ "$worst" -eq 0 ]; then
  echo "::warning title=aval publish::no aval-* artifacts in this workflow run: nothing to publish"
fi
exit "$worst"
