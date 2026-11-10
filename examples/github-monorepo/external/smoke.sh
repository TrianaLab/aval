#!/usr/bin/env bash
# Calls a service this repository does not control: its result can change
# with the same inputs, so CI always runs it.
set -euo pipefail
curl -fsS -o /dev/null https://proxy.golang.org/
echo "external smoke: ok"
