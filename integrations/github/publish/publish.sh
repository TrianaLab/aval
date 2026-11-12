#!/bin/bash
# Steps 3, 4, 6 and 7 of the aval publish action (#212); it runs only
# when there is an aval-* artifact (#260). Reads INPUT_FILE,
# INPUT_POLICY, INPUT_STATS, INPUT_STATS_REGISTRY, AVAL (absolute path of the installed binary),
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
# Where run records go (#290): empty means the policy's registry. The same
# host/path check as policy-registry.sh: no scheme, no credentials, no @.
stats_registry=${INPUT_STATS_REGISTRY:-}
if [ -n "$stats_registry" ] && ! [[ "$stats_registry" =~ ^[A-Za-z0-9.-]+(:[0-9]+)?/[a-z0-9._/-]+$ ]]; then
  echo "::error title=aval publish::stats-registry: \"$stats_registry\" must be host[:port]/path"
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
if ! registry=$(/bin/bash "${BASH_SOURCE[0]%/*}/../policy-registry.sh" "$policy" 2>&1); then
  echo "::error title=aval publish::the trust policy at $INPUT_POLICY has no valid registry field: \"$registry\""
  exit 1
fi
echo "trust policy: $INPUT_POLICY from the default branch ($DEFAULT_BRANCH), registry $registry"
stats_registry=${stats_registry:-$registry}

# Step 4: credentials for the registry host only, in a credential store of
# this step, never the job's. A stats registry on another host gets none.
export DOCKER_CONFIG="$RUNNER_TEMP/aval-publish-docker"
trap '/bin/rm -rf "$DOCKER_CONFIG"' EXIT
/bin/mkdir -p "$DOCKER_CONFIG"
if [ -n "${REGISTRY_USERNAME:-}" ] && [ -n "${REGISTRY_PASSWORD:-}" ]; then
  printf '%s:%s' "$REGISTRY_USERNAME" "$REGISTRY_PASSWORD" | /usr/bin/base64 | /usr/bin/tr -d '\n' |
    /usr/bin/jq -Rc --arg host "${registry%%/*}" '{auths: {($host): {auth: .}}}' > "$DOCKER_CONFIG/config.json"
fi

# v0.8.1 and older have no push --outputs-dir (#236): they push a test
# without outputs as before, and a test with outputs is refused below, never
# pushed with the checkout's files.
help=$("$AVAL" push --help) || help=""
outputs_dir=false
if [[ "$help" == *--outputs-dir* ]]; then outputs_dir=true; fi
# The suite job's env section (#247): environment.env is hashed where the
# test ran, not in this job. A release without push --env-file hashes this
# job's environment, as before.
env_file=false
if [[ "$help" == *--env-file* ]]; then env_file=true; fi

# Step 6: one push per artifact, each with its own token.
worst=0
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
  elif extra=$(/usr/bin/find "$dir" -mindepth 1 -maxdepth 1 ! -name result.json ! -name report.json ! -name record.json ! -name env.txt ! -name outputs -print -quit) && [ -n "$extra" ]; then
    echo "::error title=aval publish::artifact aval-$name holds ${extra##*/}, which is not part of the artifact layout"
    code=1
  elif [ "$outputs_dir" = false ] && ! expanded=$("$AVAL" explain -f "$INPUT_FILE" --test "$name" --expand); then
    echo "::error title=aval publish::$name: aval explain could not read the test, so the action cannot tell whether it declares outputs"
    code=1
  elif [ "$outputs_dir" = false ] && [[ $'\n'"$expanded" == *$'\n'outputs:* ]]; then
    echo "::error title=aval publish::$name declares outputs and the installed aval has no push --outputs-dir: publishing them from the artifact needs aval v0.8.2 or later (#236)"
    code=1
  elif ! token=$(printf 'header = "Authorization: Bearer %s"\n' "$ACTIONS_ID_TOKEN_REQUEST_TOKEN" |
    /usr/bin/curl -fsS --retry 3 -K - "$ACTIONS_ID_TOKEN_REQUEST_URL&audience=sigstore" | /usr/bin/jq -er '.value'); then
    echo "::error title=aval publish::$name: could not get an OIDC token for Sigstore"
    code=1
  else
    args=(push -f "$INPUT_FILE" --test "$name" --registry "$registry" --report-file "${dir}report.json" --result-file "${dir}result.json")
    # aval push checks every file under outputs/ against the contract's
    # outputs globs and refuses anything else, or a missing outputs/ when
    # the contract declares outputs (#236).
    if [ "$outputs_dir" = true ]; then args+=(--outputs-dir "${dir}outputs"); fi
    if [ "$env_file" = true ] && [ -f "${dir}env.txt" ]; then args+=(--env-file "${dir}env.txt"); fi
    SIGSTORE_ID_TOKEN="$token" "$AVAL" "${args[@]}" || code=$?
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

  # Step 7: every artifact's record, hit, miss or verification failure,
  # whatever push did (#290). Unsigned statistics that run and verify never
  # read; best effort, never fails the step.
  if [ "${INPUT_STATS:-false}" = true ] && [ -f "${dir}record.json" ]; then
    "$AVAL" stats publish --registry "$stats_registry" --source-repo "$GITHUB_REPOSITORY" "${dir}record.json" ||
      echo "::warning title=aval publish::$name: could not publish the run record"
  fi
done
exit "$worst"
