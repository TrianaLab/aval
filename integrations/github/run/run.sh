#!/bin/bash
# Steps 3 to 7 of the aval run action (#211). Reads INPUT_TEST (one test, or
# several names and glob patterns, #327), INPUT_FILE,
# INPUT_POLICY, INPUT_SHADOW, INPUT_LOOKUP_ONLY, INPUT_MIN_TRUST_LEVEL, AVAL (absolute path of
# the installed binary), DEFAULT_BRANCH, GH_TOKEN, REGISTRY_USERNAME,
# REGISTRY_PASSWORD and the runner's GITHUB_* variables.
#
# The trust policy is fetched from the default branch through the contents
# API on every event, never read from the checkout, so a pull request that
# edits it does not change what its own run trusts. The registry comes from
# inside the policy (#209); AVAL_REGISTRY and AVAL_POLICY from the job are
# ignored; AVAL_MIN_TRUST_LEVEL is not, since it can only raise the policy's
# minimum (#214). On the default branch the minimum is raised to runner unless
# the min-trust-level input says otherwise (#251), so main never goes green on
# a developer result that aval push would then not publish. No policy means
# nothing is reused. Tools are called by absolute path, so an earlier step
# that writes GITHUB_PATH cannot shim them. A hostile pull request still
# controls its own workflow file: required code-owner review is what protects
# the default branch (docs/trust-model.md).
#
# Several tests (#327) run one after the other in this step, each with its
# own lookup, verify, run and report; after them all, the step exits with the
# worst code, and 5 (a security event) is never downgraded.
set -euo pipefail
: "${INPUT_TEST:?}" "${INPUT_FILE:?}" "${INPUT_POLICY:?}" "${AVAL:?}" "${GH_TOKEN:?}"
: "${GITHUB_API_URL:?}" "${GITHUB_REPOSITORY:?}" "${GITHUB_EVENT_NAME:?}" "${RUNNER_TEMP:?}" "${GITHUB_OUTPUT:?}" "${GITHUB_STEP_SUMMARY:?}"
unset AVAL_REGISTRY AVAL_POLICY

# test: names and glob patterns, split on whitespace and commas. A name
# follows metadata.name's syntax (internal/contract): it names the artifact
# and a path. A pattern (* ? [) must match at least one test of the contract.
name_re='^[a-z0-9]([a-z0-9._-]{0,62}[a-z0-9])?$'
pattern_re='^[][a-z0-9._*?!-]+$'
read -ra entries <<< "${INPUT_TEST//[$',\n\r']/ }"
if [ "${#entries[@]}" -eq 0 ]; then
  echo "::error title=aval run::test: name at least one test"
  exit 2
fi
for e in "${entries[@]}"; do
  if [[ "$e" == *[]*?[]* ]]; then
    re=$pattern_re kind="a valid pattern (lowercase names with * ? [...])"
  else
    re=$name_re kind="a valid metadata.name"
  fi
  if ! [[ "$e" =~ $re ]]; then
    echo "::error title=aval run::test: \"$e\" is not $kind"
    exit 2
  fi
done
if ! [[ "$INPUT_POLICY" =~ ^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$ ]] || [[ "/$INPUT_POLICY/" == */../* ]]; then
  echo "::error title=aval run::policy: \"$INPUT_POLICY\" must be a relative path in the repository"
  exit 2
fi
case "${INPUT_SHADOW:-false}" in
  true | false) ;;
  *)
    echo "::error title=aval run::shadow: \"$INPUT_SHADOW\" must be true or false"
    exit 2
    ;;
esac
# Lookup-only runs aval verify instead of aval run (#252): the same policy
# and minimum, but nothing runs, uploads or writes .aval/. Its outcome only
# lets a job skip setup steps; the run step after it still decides reuse.
lookup=${INPUT_LOOKUP_ONLY:-false}
case "$lookup" in
  true | false) ;;
  *)
    echo "::error title=aval run::lookup-only: \"$lookup\" must be true or false"
    exit 2
    ;;
esac
if [ "$lookup" = true ] && [ "${INPUT_SHADOW:-false}" = true ]; then
  echo "::error title=aval run::lookup-only: cannot be combined with shadow: true, which runs the test"
  exit 2
fi
rank() { case "$1" in developer) echo 1 ;; runner) echo 2 ;; isolated) echo 3 ;; *) echo 9 ;; esac; }
case "${INPUT_MIN_TRUST_LEVEL:-auto}" in
  auto | developer | runner | isolated) ;;
  *)
    echo "::error title=aval run::min-trust-level: \"$INPUT_MIN_TRUST_LEVEL\" must be auto, developer, runner or isolated"
    exit 2
    ;;
esac
if [ -n "${AVAL_MIN_TRUST_LEVEL:-}" ] && [ "$(rank "$AVAL_MIN_TRUST_LEVEL")" = 9 ]; then
  echo "::error title=aval run::AVAL_MIN_TRUST_LEVEL: \"$AVAL_MIN_TRUST_LEVEL\" must be developer, runner or isolated"
  exit 2
fi

# The tests, in the order named; a pattern adds the contract's tests it
# matches, in contract order. aval explain --all lists the names (it exits 1
# when a test cannot be fingerprinted, but still lists it).
tests=() seen=" " known=""
for e in "${entries[@]}"; do
  if [[ "$e" != *[]*?[]* ]]; then
    if [[ "$seen" != *" $e "* ]]; then tests+=("$e") seen+="$e "; fi
    continue
  fi
  if [ -z "$known" ]; then
    known=$("$AVAL" explain -f "$INPUT_FILE" --all -o json 2> /dev/null | /usr/bin/jq -r '.tests[].name' 2> /dev/null || true)
  fi
  matched=false
  while IFS= read -r n; do
    # shellcheck disable=SC2053 # $e is the glob
    if [ -n "$n" ] && [[ "$n" == $e ]]; then
      matched=true
      if [[ "$seen" != *" $n "* ]]; then tests+=("$n") seen+="$n "; fi
    fi
  done <<< "$known"
  if [ "$matched" = false ]; then
    echo "::error title=aval run::test: \"$e\" matches no test in $INPUT_FILE"
    exit 2
  fi
done

# A literal string comparison: no glob, no prefix match, and an empty
# default branch never matches.
on_default=false
if [ -n "${DEFAULT_BRANCH:-}" ] && [ "${GITHUB_REF:-}" = "refs/heads/$DEFAULT_BRANCH" ]; then
  on_default=true
fi
# The requested minimum: the input (runner on the default branch under auto)
# or the job's AVAL_MIN_TRUST_LEVEL, whichever is higher. Empty keeps the
# policy's. Step 7 raises it to the policy's minimum if that is higher.
want="${INPUT_MIN_TRUST_LEVEL:-auto}"
if [ "$want" = auto ]; then
  want=""
  if [ "$on_default" = true ]; then want=runner; fi
fi
job="${AVAL_MIN_TRUST_LEVEL:-}"
unset AVAL_MIN_TRUST_LEVEL
if [ -n "$job" ] && { [ -z "$want" ] || [ "$(rank "$job")" -gt "$(rank "$want")" ]; }; then
  want=$job
fi

# Step 6: only a push to the default branch hands its result to a publish job.
upload=false
if [ "$lookup" = false ] && [ "$GITHUB_EVENT_NAME" = push ] && [ "$on_default" = true ]; then
  upload=true
fi
# One test uploads aval-<test>, as before. Several share one artifact,
# aval-.<id> (an id no metadata.name can be), holding a <test>/ directory
# each, which the publish action pushes test by test. The id tells the jobs
# of one workflow run apart.
# ponytail: CRC32 of the test list; two jobs with different lists colliding
# is negligible, a longer hash if it ever happens.
if [ "${#tests[@]}" -eq 1 ]; then
  echo "artifact=aval-${tests[0]}" >> "$GITHUB_OUTPUT"
  echo "artifact-path=.aval/${tests[0]}/" >> "$GITHUB_OUTPUT"
else
  id=$(printf '%s\n' "${tests[@]}" | /usr/bin/cksum)
  echo "artifact=aval-.${id%% *}" >> "$GITHUB_OUTPUT"
  { echo "artifact-path<<AVAL_EOF"; printf '.aval/%s/\n' "${tests[@]}"; echo AVAL_EOF; } >> "$GITHUB_OUTPUT"
fi
echo "upload=$upload" >> "$GITHUB_OUTPUT"

# Step 3: the policy from the default branch.
# Named after the first test: one test keeps the path it always had.
policy="$RUNNER_TEMP/aval-policy-${tests[0]}.yaml"
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
# The test runs pull request code: it never sees the job token (#250).
unset GH_TOKEN
# The flags every test gets; step 4 adds the command and --test.
args=()
# Shadow mode verifies a published result and runs the test anyway (#216).
if [ "${INPUT_SHADOW:-false}" = true ]; then
  args+=(--shadow)
fi
if [ "$lookup" = false ] && [ -n "${GITHUB_RUN_ID:-}" ]; then
  args+=(--ci-run-url "${GITHUB_SERVER_URL:-https://github.com}/$GITHUB_REPOSITORY/actions/runs/$GITHUB_RUN_ID")
fi
if [ "$status" = 200 ]; then
  args+=(--policy "$policy")
  echo "trust policy: $INPUT_POLICY from the default branch ($DEFAULT_BRANCH)"
else
  /bin/rm -f "$policy"
  echo "::warning title=aval run::no trust policy at $INPUT_POLICY in the default branch (${DEFAULT_BRANCH:-unknown}, HTTP $status): nothing is reused, the test runs"
fi

# Registry credentials (#250), opt-in. They go to the policy registry's host
# only, in a credential store of this step that starts from the job's auths
# (so aval keeps the job's logins) and is deleted on exit. aval reads it
# through AVAL_DOCKER_CONFIG, which runner.command never gets: the test keeps
# the job's DOCKER_CONFIG. A missing policy or registry field, or an aval
# without AVAL_DOCKER_CONFIG, adds none.
username=${REGISTRY_USERNAME:-} password=${REGISTRY_PASSWORD:-}
unset REGISTRY_USERNAME REGISTRY_PASSWORD
if [ -n "$username" ] || [ -n "$password" ]; then
  if [ -z "$username" ] || [ -z "$password" ]; then
    echo "::warning title=aval run::set both registry-username and registry-password: the registry is read anonymously"
  elif [ ! -f "$policy" ]; then
    echo "::warning title=aval run::no trust policy, so no registry to send credentials to: the registry is read anonymously"
  elif ! registry=$(/bin/bash "${BASH_SOURCE[0]%/*}/../policy-registry.sh" "$policy" 2>&1); then
    echo "::warning title=aval run::the trust policy at $INPUT_POLICY has no valid registry field: no credentials are sent"
  elif [[ "$("$AVAL" run --help 2> /dev/null || true)" != *AVAL_DOCKER_CONFIG* ]]; then
    echo "::warning title=aval run::the installed aval has no AVAL_DOCKER_CONFIG, which keeps the credentials from the test: the registry is read anonymously"
  else
    jobconf="${DOCKER_CONFIG:-${HOME:-}/.docker}/config.json"
    creds=$(/usr/bin/mktemp -d "$RUNNER_TEMP/aval-run-docker.XXXXXX")
    trap '/bin/rm -rf "$creds"' EXIT
    { /usr/bin/jq -cs 'if (.[0].auths | type) == "object" then .[0].auths else {} end' "$jobconf" 2> /dev/null || echo '{}'; } > "$creds/job.json"
    printf '%s:%s' "$username" "$password" | /usr/bin/base64 | /usr/bin/tr -d '\n' |
      /usr/bin/jq -Rc --arg host "${registry%%/*}" --slurpfile job "$creds/job.json" \
        '{auths: ($job[0] + {($host): {auth: .}})}' > "$creds/config.json"
    /bin/rm -f "$creds/job.json"
    export AVAL_DOCKER_CONFIG="$creds"
    echo "registry credentials: for ${registry%%/*} only"
  fi
fi
unset username password

# Step 7: a policy that trusts pull request workflows at or above
# minTrustLevel lets a pull request sign results that are reused.
# ponytail: line-based read of block-style YAML (what aval init writes); flow
# style is not parsed, and the check is a warning, never a gate.
if [ -f "$policy" ]; then
  sample="https://github.com/${GITHUB_WORKFLOW_REF:-$GITHUB_REPOSITORY/.github/workflows/ci.yaml@refs/heads/main}"
  sample="${sample%@*}@refs/pull/1/merge"
  min=9 minname=""
  while IFS=$'\t' read -r level re; do
    if [ "$level" = min ]; then
      min=$(rank "$re") minname=$re
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
  # Never below the policy's minimum. An unparsed minimum (9) leaves the
  # requested level to aval, which exits 2 if it is below: fail closed.
  if [ -n "$want" ] && [ "$min" != 9 ] && [ "$min" -gt "$(rank "$want")" ]; then
    want=$minname
  fi
  if [ -n "$want" ]; then
    args+=(--min-trust-level "$want")
    effective=$want
  elif [ "$min" != 9 ]; then
    effective=$minname
  else
    effective="the policy's"
  fi
else
  # No policy: nothing is reused, so the minimum has no effect.
  effective="none (no policy)"
fi
echo "trust minimum: $effective"

# The files the outputs globs matched travel in the artifact, for the
# publish job's aval push --outputs-dir (#236); aval resolves them. A
# release without the flag (v0.8.1 and older) still runs a test without
# outputs; for one with outputs, which aval explain --expand lists, the
# action refuses rather than upload a result whose outputs the publish job
# would take from its own checkout.
help=""
if [ "$upload" = true ]; then
  help=$("$AVAL" run --help) || help=""
fi
# outputs_dir test prints the --outputs-dir flag for test, or returns 1 when
# the test needs one the installed aval lacks.
outputs_dir() {
  if [ "$upload" = false ]; then
    return 0
  elif [[ "$help" == *--outputs-dir* ]]; then
    echo "--outputs-dir .aval/$1/outputs"
  elif ! expanded=$("$AVAL" explain -f "$INPUT_FILE" --test "$1" --expand); then
    echo "::error title=aval run::$1: aval explain could not read the test, so the action cannot tell whether it declares outputs" >&2
    return 1
  elif [[ $'\n'"$expanded" == *$'\n'outputs:* ]]; then
    echo "::error title=aval run::$1 declares outputs and the installed aval has no run --outputs-dir: carrying outputs to the publish job needs aval v0.8.2 or later (#236)" >&2
    return 1
  fi
}

orank() { case "$1" in verified) echo 0 ;; miss) echo 1 ;; error) echo 2 ;; unknown) echo 3 ;; *) echo 4 ;; esac; }
worst=0 all=verified outcomes='{}'
count=() # one "<outcome>" per test, for the summary line
mode=""
if [ "$lookup" = true ]; then mode="lookup "; fi
for test in "${tests[@]}"; do
  # Step 4. A report left by an earlier run in this workspace must not be
  # read as this run's outcome. Lookup-only keeps its report in RUNNER_TEMP;
  # with no policy there is no registry to ask, and nothing is reused: a miss.
  code=0
  if [ "$lookup" = true ]; then
    report="$RUNNER_TEMP/aval-lookup-$test.json"
    /bin/rm -f "$report"
    if [ -f "$policy" ]; then
      "$AVAL" verify -f "$INPUT_FILE" --test "$test" -o json ${args[@]+"${args[@]}"} > "$report" || code=$?
    else
      code=4
    fi
  else
    report=".aval/$test/report.json"
    /bin/rm -f "$report"
    if flag=$(outputs_dir "$test"); then
      # shellcheck disable=SC2086 # $flag is empty or two words without spaces
      "$AVAL" run -f "$INPUT_FILE" --test "$test" --source-repo "$GITHUB_REPOSITORY" $flag ${args[@]+"${args[@]}"} || code=$?
    else
      code=1
    fi
  fi

  # Step 5: the summary prints only known values; the reason can carry text
  # from an untrusted attestation, so it stays in the report.
  outcome=unknown result=""
  if [ -f "$report" ]; then
    outcome=$(/usr/bin/jq -r '.outcome // ""' "$report" 2> /dev/null) || outcome=unknown
    result=$(/usr/bin/jq -r '.result // ""' "$report" 2> /dev/null) || result=""
  fi
  case "$outcome" in verified | miss | verification-failed | error) ;; *) outcome=unknown ;; esac
  case "$result" in passed | failed | "") ;; *) result=unknown ;; esac
  # Exit 5 is a security event whatever the report's lookup outcome says (#195).
  if [ "$code" -eq 5 ]; then outcome=verification-failed; fi
  # A miss is routine, so the lookup step succeeds; exit 5 never becomes one.
  if [ "$lookup" = true ] && [ "$code" -eq 4 ]; then outcome=miss; fi
  echo "### aval $mode$test: \`$outcome\` $result (exit $code)" >> "$GITHUB_STEP_SUMMARY"
  if [ "$code" -eq 5 ]; then
    echo "::error title=aval SECURITY::$test: an attestation for this fingerprint failed signature or policy verification; see the SECURITY line in the log and the uploaded report"
  fi
  if [ "$lookup" = true ] && [ "$code" -eq 4 ]; then code=0; fi
  if [ "$code" -eq 5 ] || { [ "$worst" -ne 5 ] && [ "$code" -gt "$worst" ]; }; then
    worst=$code
  fi
  if [ "$(orank "$outcome")" -gt "$(orank "$all")" ]; then all=$outcome; fi
  outcomes=$(/usr/bin/jq -cn --argjson m "$outcomes" --arg t "$test" --arg o "$outcome" '$m + {($t): $o}')
  count+=("$outcome")
done

# outcome is the worst of the tests: verified only when every test is.
echo "outcome=$all" >> "$GITHUB_OUTPUT"
echo "outcomes=$outcomes" >> "$GITHUB_OUTPUT"
if [ "${#tests[@]}" -gt 1 ]; then
  tally=$(printf '%s\n' "${count[@]}" | /usr/bin/sort | /usr/bin/uniq -c | /usr/bin/awk '{printf "%s%s %s", sep, $1, $2; sep=", "}')
  echo "${#tests[@]} tests: $tally (exit $worst)" >> "$GITHUB_STEP_SUMMARY"
fi
echo "Trust minimum: $effective" >> "$GITHUB_STEP_SUMMARY"
exit "$worst"
