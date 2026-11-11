#!/bin/bash
# The aval publish action (#260), after the download: any=true in GITHUB_OUTPUT when
# ARTIFACTS holds an aval-* directory. Without one, the action installs
# nothing, fetches no policy, requests no OIDC token and exits 0: a run whose
# test jobs were skipped has nothing to sign.
set -euo pipefail
: "${ARTIFACTS:?}" "${GITHUB_OUTPUT:?}"
for dir in "$ARTIFACTS"/aval-*/; do
  if [ -d "$dir" ]; then
    echo "any=true" >> "$GITHUB_OUTPUT"
    exit 0
  fi
done
echo "any=false" >> "$GITHUB_OUTPUT"
echo "::notice title=aval publish::no aval-* artifacts in this workflow run: nothing to publish"
