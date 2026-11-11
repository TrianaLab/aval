#!/bin/bash
# Usage: pin-examples.sh <commit> <tag> <workflow>...
# Points each step that uses TrianaLab/aval/integrations/github/{run,publish}
# in the example workflows at <commit>, with a "# <tag>" comment, unless the
# step already says "# <tag>" (#261). <commit> must be a main commit whose
# actions pin <tag>. Fails, changing nothing, when the commit or the tag is
# malformed. The pin-examples job of .github/workflows/release.yaml runs it;
# tests/architecture checks the result (TestExampleActionPins) and
# tests/release the script (TestPinExamplesScript).
set -euo pipefail
[ $# -ge 3 ] || { echo "usage: pin-examples.sh <commit> <tag> <workflow>..." >&2; exit 2; }
sha=$1 tag=$2
shift 2
[[ "$sha" =~ ^[0-9a-f]{40}$ ]] || { echo "bad commit: $sha" >&2; exit 1; }
[[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo "bad tag: $tag" >&2; exit 1; }
for f in "$@"; do
  sed -E "/ # ${tag//./\\.}\$/! s|(uses: TrianaLab/aval/integrations/github/[a-z]+)@[^ ]+( #.*)?\$|\1@$sha # $tag|" "$f" > "$f.tmp"
  mv "$f.tmp" "$f"
done
