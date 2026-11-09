#!/bin/sh
# Builds the docs/quickstart.md project in "$1" (step 1 of the page),
# plus other.key for tampered.tape. `make docs-demos` runs it before the tapes.
# Keep it in sync with docs/quickstart.md step 1.
set -eu
cd "$1"
mkdir src && echo hello > src/hello.txt
aval init --name hello --source 'src/**' --cacheability hermetic --key aval.key -- grep -q hello src/hello.txt >/dev/null
aval keygen other >/dev/null
