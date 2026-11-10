#!/bin/bash
# Step 1 of the aval run action (#211): refuse what would let untrusted code
# sign or read secrets. pull_request_target and workflow_run run with the
# base repository's token and secrets while checking out code a pull request
# controls. ACTIONS_ID_TOKEN_REQUEST_URL is set only when the job has
# `id-token: write`: a suite job must not be able to sign, so signing stays in
# a separate publish job (split credentials).
set -euo pipefail
case "${GITHUB_EVENT_NAME:?}" in
  pull_request_target | workflow_run)
    echo "::error title=aval run::refusing to run on $GITHUB_EVENT_NAME: it runs code a pull request controls with the base repository's credentials; use pull_request"
    exit 1
    ;;
esac
if [ -n "${ACTIONS_ID_TOKEN_REQUEST_URL:-}" ]; then
  echo "::error title=aval run::this job has id-token: write; run tests without it and sign in a separate publish job (split credentials)"
  exit 1
fi
