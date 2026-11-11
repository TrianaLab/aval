#!/bin/bash
# Prints the top-level registry: field of the trust policy file $1, the one
# the run and publish actions fetched from the default branch, and exits 1
# with the raw value on stderr when it is missing or not host[:port]/path.
# The host part is where the actions write registry credentials (#212, #250),
# so the check is strict: no scheme, no userinfo, no whitespace.
set -euo pipefail
# ponytail: line-based read of the top-level registry: key in block-style
# YAML (what aval init writes); aval validates the value again.
registry=$(/usr/bin/awk '
  /^registry:/ {
    s = $0; sub(/^registry:[ \t]*/, "", s)
    if (s ~ /^".*"$/ || s ~ /^'"'"'.*'"'"'$/) s = substr(s, 2, length(s) - 2)
    else sub(/[ \t]+#.*$/, "", s)
    print s; exit
  }' "$1")
if ! [[ "$registry" =~ ^[A-Za-z0-9.-]+(:[0-9]+)?/[a-z0-9._/-]+$ ]]; then
  printf '%s\n' "$registry" >&2
  exit 1
fi
printf '%s\n' "$registry"
