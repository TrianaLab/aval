#!/bin/sh
# Builds the docs/quickstart.md project in "$1" (steps 2 and 3 of the page),
# plus other.key for tampered.tape. `make docs-demos` runs it before the tapes.
# Keep it in sync with docs/quickstart.md steps 2 and 3.
set -eu
cd "$1"
mkdir src && echo hello > src/hello.txt
cat > aval.yaml <<'YAML'
apiVersion: aval.trianalab.io/v1
kind: Test
metadata:
  name: hello
runner:
  command: ["grep", "-q", "hello", "src/hello.txt"]
scope:
  source: ["src/**"]
cacheability: hermetic
YAML
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out aval.key 2>/dev/null
openssl pkey -in aval.key -pubout -out aval.pub
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out other.key 2>/dev/null
cat > policy.yaml <<'YAML'
apiVersion: aval.trianalab.io/v1
kind: Policy
minTrustLevel: developer
identities:
  - publicKey: aval.pub
    trustLevel: developer
YAML
