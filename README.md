# ![Aval](docs/assets/images/wordmark.svg#gh-light-mode-only)![Aval](docs/assets/images/wordmark-dark.svg#gh-dark-mode-only)

Test results as signed, verifiable OCI artifacts.

Aval is not a test cache. It is a CI primitive for **verifiable test execution reuse**: a heavy test runs once, the result is signed and bound to a fingerprint of its declared inputs, and any other environment with the same fingerprint can verify it and skip the run. A signature proves *who* claims the test passed, not that it ran honestly, so reuse requires an explicit input contract plus a trust policy over signers.

**Problem.** Integration and end-to-end suites run again on every push, in every pipeline, even when nothing they depend on changed. Build caches cover compilation and unit tests well, but rarely heavy tests with services, and they give you no signed record of who ran what.

## How it works

```text
dev / CI:   run test -> fingerprint inputs -> sign (in-toto/DSSE) -> push to OCI registry
                                                                     tag sha256-<fp>
CI on push: fingerprint inputs -> lookup sha256-<fp> -> verify signature + policy -> reuse
                                         | miss                    | fails
                                         v                         v
                                      run test           run test, report security event
```

No server or database: ORAS, Cosign and OCI 1.1 referrers do the work, and artifacts are always consumed by digest. If input equivalence cannot be proven, the test runs.

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/TrianaLab/aval/main/scripts/get-aval.sh | bash
aval --version
```

On Windows, in PowerShell: `irm https://raw.githubusercontent.com/TrianaLab/aval/main/scripts/install.ps1 | iex`.
Both installers check the sha256 against the release's `checksums.txt`, and the
release signature and provenance when `cosign` or `gh` is installed; see
[Install](docs/install.md) for options.

Alternatives: download a release archive (Linux and macOS, amd64 or arm64; Windows has a zip):

```sh
VERSION=0.2.0
OS=linux        # or darwin
ARCH=amd64      # or arm64
curl -fsSLO "https://github.com/TrianaLab/aval/releases/download/v$VERSION/aval_${VERSION}_${OS}_${ARCH}.tar.gz"
tar -xzf "aval_${VERSION}_${OS}_${ARCH}.tar.gz" aval
sudo mv aval /usr/local/bin/
```

Or build it with Go 1.27 or newer: `go install github.com/TrianaLab/aval/cmd/aval@latest`.
[CONTRIBUTING.md](CONTRIBUTING.md#changesets-and-releases) shows how to check the release signature.

## Try it on your laptop

This path runs a test, signs the result with a local key, pushes it to a local
registry and verifies it. You need `aval` (one line, see [Install](#install)),
Docker and `openssl`. Copy each block into one shell, in order.

<!-- quickstart:start (tests/acceptance/readme_test.go runs these blocks) -->

Start a local OCI registry. Port 5001, because macOS uses 5000 for AirPlay:

```sh
docker run -d --name aval-registry -p 5001:5000 registry:2
```

Make a project with one input file and a contract. Every glob in `scope` must
match at least one file, or Aval refuses to fingerprint (and the test always
runs):

```sh
mkdir aval-demo && cd aval-demo
mkdir src && echo hello > src/hello.txt
cat > aval.yaml <<'EOF'
apiVersion: aval.trianalab.io/v1
kind: Test
metadata:
  name: hello
runner:
  command: ["grep", "-q", "hello", "src/hello.txt"]
scope:
  source: ["src/**"]
cacheability: hermetic
EOF
```

Make a P-256 signing key and a trust policy that trusts it. Aval only accepts
ECDSA P-256 keys:

```sh
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out aval.key
openssl pkey -in aval.key -pubout -out aval.pub
cat > policy.yaml <<'EOF'
apiVersion: aval.trianalab.io/v1
kind: Policy
minTrustLevel: developer
identities:
  - publicKey: aval.pub
    trustLevel: developer
EOF
```

Run the test, then sign and push the result. The output says `miss`:
this run has no policy, so it does not look for a result and runs the test:

```sh
aval run --push --key aval.key --registry localhost:5001/demo/hello-aval
```

Check that a trusted result exists for these exact inputs. It prints
`verified hello sha256:...` and exits 0:

```sh
aval verify --registry localhost:5001/demo/hello-aval --policy policy.yaml
```

Run again: Aval finds the verified result and skips the test:

```sh
aval run --registry localhost:5001/demo/hello-aval --policy policy.yaml
```

<!-- quickstart:end -->

Now change `src/hello.txt` and run `aval verify` again: the fingerprint
changes, so it reports `miss` and exits 4. Stop the registry with
`docker rm -f aval-registry`.

## Use it in CI

In CI, sign without a key: leave out `--key` and put an OIDC token in
`$SIGSTORE_ID_TOKEN` (on GitHub Actions, request `id-token: write`). The policy
then trusts the CI identity instead of a key file; see the
[trust model](docs/trust-model.md#policy-file). Pushing to a real registry uses
your Docker credentials (`docker login`); see
[registry credentials](docs/artifact-format.md#registry-credentials).

```sh
aval run --push --registry ghcr.io/acme/orders-aval --policy policy.yaml   # reuse a verified result, or run and publish
aval verify --registry ghcr.io/acme/orders-aval --policy policy.yaml       # exit 0 verified, 4 miss, 5 verification failed
```

All exit codes are in the [CLI reference](docs/cli-reference.md#exit-codes).

## Status: pre-alpha

Contract, fingerprint, signing (keyless and key-based), push to and lookup in an OCI registry, verification against a trust policy and `aval run` reuse or execute work. Formats are frozen at `aval.trianalab.io/v1` (contract and verify report) and predicate `execution/v2`; see [compatibility rules](docs/artifact-format.md#versioning-and-compatibility). `aval run --shadow` verifies and still runs the test, and fails on a false hit.

## Docs

[Install](docs/install.md) · [Vision](docs/vision.md) · [Trust model](docs/trust-model.md) · [Artifact format](docs/artifact-format.md) · [Contract](docs/contract.md) · [Integrations](docs/integrations.md) · [CLI reference](docs/cli-reference.md) · [Example: Go + Compose](examples/go-compose)

## License

Apache-2.0. See [LICENSE](LICENSE).
