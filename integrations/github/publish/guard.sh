#!/bin/bash
# Step 1 of the aval publish action (#212): sign only from the default
# branch, which branch protection and code-owner review guard. Another ref
# (a pull request, a feature branch) needs allow-unprotected: true.
# pull_request_target and workflow_run are always refused: they run with the
# base repository's identity on artifacts that code a pull request controls
# may have produced. Signing is keyless with the job's OIDC token, so the job
# needs id-token: write. Reads ALLOW_UNPROTECTED, DEFAULT_BRANCH and the
# runner's GITHUB_* variables.
set -euo pipefail
case "${GITHUB_EVENT_NAME:?}" in
  pull_request_target | workflow_run)
    echo "::error title=aval publish::refusing to publish on $GITHUB_EVENT_NAME: it signs with the base repository's identity what a pull request may control"
    exit 1
    ;;
esac
case "${ALLOW_UNPROTECTED:-false}" in
  true) ;;
  false)
    if [ -z "${DEFAULT_BRANCH:-}" ] || [ "${GITHUB_REF:-}" != "refs/heads/$DEFAULT_BRANCH" ]; then
      echo "::error title=aval publish::refusing to publish from ${GITHUB_REF:-an unknown ref}: only the default branch (${DEFAULT_BRANCH:-unknown}) publishes; set allow-unprotected: true to publish from another ref"
      exit 1
    fi
    ;;
  *)
    echo "::error title=aval publish::allow-unprotected: \"$ALLOW_UNPROTECTED\" must be true or false"
    exit 2
    ;;
esac
if [ -z "${ACTIONS_ID_TOKEN_REQUEST_URL:-}" ] || [ -z "${ACTIONS_ID_TOKEN_REQUEST_TOKEN:-}" ]; then
  echo "::error title=aval publish::this job has no OIDC token: give it permissions: {contents: read, packages: write, id-token: write}"
  exit 1
fi
