#!/usr/bin/env bash
# Usage: summary.sh <name> <report.json>
# Adds one `aval run --report` result to the job summary. Only the outcome and
# result are printed, and only known values: the reason can carry text from an
# untrusted attestation, so it stays in the uploaded report. A verification
# failure, as the outcome or among the rejected candidates, is annotated as an
# error and fails the step (invariant 4).
set -euo pipefail
name=$1 report=$2
if [ ! -f "$report" ]; then
  echo "### $name: no report" >> "$GITHUB_STEP_SUMMARY"
  exit 0
fi
outcome=$(jq -r '.outcome // ""' "$report")
case "$outcome" in verified | miss | verification-failed | error) ;; *) outcome=unknown ;; esac
result=$(jq -r '.result // ""' "$report")
case "$result" in passed | failed | "") ;; *) result=unknown ;; esac
echo "### $name: \`$outcome\` $result" >> "$GITHUB_STEP_SUMMARY"
# A rejected candidate that failed verification is a security event even
# beside a verified result (#135).
if [ "$outcome" = verification-failed ] || jq -e 'any(.rejected[]?; .outcome == "verification-failed")' "$report" > /dev/null; then
  echo "::error title=aval verification failed ($name)::security event: an attestation for this fingerprint failed signature or policy verification; see the SECURITY line in the log and the uploaded report"
  exit 1
fi
