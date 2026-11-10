#!/usr/bin/env bash
# The end-to-end suite in three shards. In a real project each shard creates
# a kind cluster, loads the image Dockerfile builds and runs a third of the
# scenarios; here each shard checks the same inputs offline.
set -euo pipefail
shard=${1:?usage: e2e/run.sh <1|2|3>}
case "$shard" in
  1) go build -o /dev/null ./cmd/monorepo ;;
  2) test "$(grep -l '^image: ' data/fixtures/*.yaml | wc -l)" -ge 2 ;;
  3) ! grep '^FROM ' Dockerfile | grep -v '@sha256:' ;;
  *)
    echo "e2e: no shard $shard" >&2
    exit 2
    ;;
esac
echo "e2e shard $shard: ok"
