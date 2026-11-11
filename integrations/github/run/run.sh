#!/bin/bash
# Steps 3 to 7 of the aval run action (#211). Reads INPUT_TEST, INPUT_FILE,
# INPUT_POLICY, AVAL (absolute path of the installed binary), DEFAULT_BRANCH,
# GH_TOKEN and the runner's GITHUB_* variables.
#
# The trust policy is fetched from the default branch through the contents
# API on every event, never read from the checkout, so a pull request that
# edits it does not change what its own run trusts. The registry comes from
# inside the policy (#209); AVAL_REGISTRY and AVAL_POLICY from the job are
# ignored. No policy means nothing is reused. Tools are called by absolute
# path, so an earlier step that writes GITHUB_PATH cannot shim them. A hostile
# pull request still controls its own workflow file: required code-owner
# review is what protects the default branch (docs/trust-model.md).
set -euo pipefail
: "${INPUT_TEST:?}" "${INPUT_FILE:?}" "${INPUT_POLICY:?}" "${AVAL:?}" "${GH_TOKEN:?}"
: "${GITHUB_API_URL:?}" "${GITHUB_REPOSITORY:?}" "${GITHUB_EVENT_NAME:?}" "${RUNNER_TEMP:?}" "${GITHUB_OUTPUT:?}" "${GITHUB_STEP_SUMMARY:?}"
unset AVAL_REGISTRY AVAL_POLICY

# metadata.name's syntax (internal/contract): it names the artifact and a path.
if ! [[ "$INPUT_TEST" =~ ^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$ ]]; then
  echo "::error title=aval run::test: \"$INPUT_TEST\" is not a valid metadata.name"
  exit 2
fi
if ! [[ "$INPUT_POLICY" =~ ^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$ ]] || [[ "/$INPUT_POLICY/" == */../* ]]; then
  echo "::error title=aval run::policy: \"$INPUT_POLICY\" must be a relative path in the repository"
  exit 2
fi

# Step 6: only a push to the default branch hands its result to a publish job.
upload=false
if [ "$GITHUB_EVENT_NAME" = push ] && [ -n "${DEFAULT_BRANCH:-}" ] && [ "${GITHUB_REF:-}" = "refs/heads/$DEFAULT_BRANCH" ]; then
  upload=true
fi
echo "upload=$upload" >> "$GITHUB_OUTPUT"

# Step 3: the policy from the default branch.
policy="$RUNNER_TEMP/aval-policy-$INPUT_TEST.yaml"
/bin/rm -f "$policy"
status=000
if [ -n "${DEFAULT_BRANCH:-}" ]; then
  ref=$(/usr/bin/jq -rn --arg v "$DEFAULT_BRANCH" '$v | @uri')
  # The token goes through stdin, not the command line.
  status=$(printf 'header = "Authorization: Bearer %s"\n' "$GH_TOKEN" |
    /usr/bin/curl -sS --retry 3 -K - -o "$policy" -w '%{http_code}' \
      -H "Accept: application/vnd.github.raw+json" -H "X-GitHub-Api-Version: 2022-11-28" \
      "$GITHUB_API_URL/repos/$GITHUB_REPOSITORY/contents/$INPUT_POLICY?ref=$ref") || status=000
fi
args=(run -f "$INPUT_FILE" --test "$INPUT_TEST" --source-repo "$GITHUB_REPOSITORY")
# The files the outputs globs matched travel in the artifact, for the
# publish job's aval push --outputs-dir (#236); aval resolves them. A
# release without the flag (v0.8.1 and older) still runs a test without
# outputs; for one with outputs, which aval explain --expand lists, the
# action refuses rather than upload a result whose outputs the publish job
# would take from its own checkout.
if [ "$upload" = true ]; then
  help=$("$AVAL" run --help) || help=""
  if [[ "$help" == *--outputs-dir* ]]; then
    args+=(--outputs-dir ".aval/$INPUT_TEST/outputs")
  elif ! expanded=$("$AVAL" explain -f "$INPUT_FILE" --test "$INPUT_TEST" --expand); then
    echo "::error title=aval run::$INPUT_TEST: aval explain could not read the test, so the action cannot tell whether it declares outputs"
    exit 1
  elif [[ $'\n'"$expanded" == *$'\n'outputs:* ]]; then
    echo "::error title=aval run::$INPUT_TEST declares outputs and the installed aval has no run --outputs-dir: carrying outputs to the publish job needs aval v0.8.2 or later (#236)"
    exit 1
  fi
fi
if [ -n "${GITHUB_RUN_ID:-}" ]; then
  args+=(--ci-run-url "${GITHUB_SERVER_URL:-https://github.com}/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID")
fi
if [ "$status" = 200 ]; then
  args+=(--policy "$policy")
  echo "trust policy: $INPUT_POLICY from the default branch ($DEFAULT_BRANCH)"
else
  /bin/rm -f "$policy"
  echo "::warning title=aval run::no trust policy at $INPUT_POLICY in the default branch (${DEFAULT_BRANCH:-unknown}, HTTP $status): nothing is reused, the test runs"
fi

# Step 7: a policy that trusts pull request workflows at or above
# minTrustLevel lets a pull request sign results that are reused.
# ponytail: line-based read of block-style YAML (what aval init writes); flow
# style is not parsed, and the check is a warning, never a gate.
if [ -f "$policy" ]; then
  sample="https://github.com/${GITHUB_WORKFLOW_REF:-$GITHUB_REPOSITORY/.github/workflows/ci.yaml@refs/heads/main}"
  sample="${sample%@*}@refs/pull/1/merge"
  rank() { case "$1" in developer) echo 1 ;; runner) echo 2 ;; isolated) echo 3 ;; *) echo 9 ;; esac; }
  min=9
  while IFS=$'\t' read -r level re; do
    if [ "$level" = min ]; then
      min=$(rank "$re")
      continue
    fi
    if [ "$(rank "$level")" -ge "$min" ] && [ "$(rank "$level")" -ne 9 ] &&
      { [[ "$re" == *refs/pull* || "$re" == *refs\\/pull* ]] || printf '%s\n' "$sample" | /usr/bin/grep -Eqx -- "$re" 2> /dev/null; }; then
      echo "::warning title=aval policy::$INPUT_POLICY trusts a refs/pull/ subject at trust level $level: a pull request's workflow can sign results that are reused; trust refs/heads/$DEFAULT_BRANCH only"
    fi
  done < <(/usr/bin/awk '
    function val(s) {
      sub(/^[^:]*:[ \t]*/, "", s)
      if (s ~ /^".*"$/) { s = substr(s, 2, length(s) - 2); gsub(/\\\\/, "\\", s) }
      else if (s ~ /^'"'"'.*'"'"'$/) { s = substr(s, 2, length(s) - 2) }
      else { sub(/[ \t]+#.*$/, "", s) }
      return s
    }
    function flush() { if (re != "") print lvl "\t" re; re = ""; lvl = "" }
    /^minTrustLevel:/ { print "min\t" val($0); next }
    /^[ \t]*- / { flush(); sub(/- /, "  ") }
    /^[ \t]*subjectRegexp:/ { re = val($0) }
    /^[ \t]*trustLevel:/ { lvl = val($0) }
    END { flush() }
  ' "$policy")
fi

# Step 4. A report left by an earlier run in this workspace must not be read
# as this run's outcome.
/bin/rm -f ".aval/$INPUT_TEST/report.json"
code=0
"$AVAL" "${args[@]}" || code=$?

# Step 5: the summary prints only known values; the reason can carry text
# from an untrusted attestation, so it stays in the report.
report=".aval/$INPUT_TEST/report.json"
outcome=unknown result=""
if [ -f "$report" ]; then
  outcome=$(/usr/bin/jq -r '.outcome // ""' "$report" 2> /dev/null) || outcome=unknown
  result=$(/usr/bin/jq -r '.result // ""' "$report" 2> /dev/null) || result=""
fi
case "$outcome" in verified | miss | verification-failed | error) ;; *) outcome=unknown ;; esac
case "$result" in passed | failed | "") ;; *) result=unknown ;; esac
# Exit 5 is a security event whatever the report's lookup outcome says (#195).
if [ "$code" -eq 5 ]; then outcome=verification-failed; fi
echo "outcome=$outcome" >> "$GITHUB_OUTPUT"
echo "### aval $INPUT_TEST: \`$outcome\` $result (exit $code)" >> "$GITHUB_STEP_SUMMARY"
if [ "$code" -eq 5 ]; then
  echo "::error title=aval SECURITY::$INPUT_TEST: an attestation for this fingerprint failed signature or policy verification; see the SECURITY line in the log and the uploaded report"
fi
exit "$code"
