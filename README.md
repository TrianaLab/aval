# ![Aval](docs/assets/images/wordmark.svg#gh-light-mode-only)![Aval](docs/assets/images/wordmark-dark.svg#gh-dark-mode-only)

Test results as signed, verifiable OCI artifacts.

A heavy test runs once. Aval signs the result and binds it to a fingerprint
of the test's declared inputs. Any other job with the same fingerprint can
verify the signature against its own trust policy and skip the run. If Aval
cannot prove that reuse is safe, the test runs.

No server or database: ORAS, Cosign and OCI 1.1 referrers do the work, in the
OCI registry you already have.

**Docs: [aval.run](https://aval.run/)**: [how it works](https://aval.run/main/concepts/),
[trust](https://aval.run/main/concepts/trust/), [outcomes](https://aval.run/main/concepts/outcomes/),
[CLI reference](https://aval.run/main/cli-reference/) and the specs.

## Install

```sh
curl -fsSL https://aval.run/install.sh | bash
aval --version
```

On Windows, in PowerShell: `irm https://aval.run/install.ps1 | iex`.
Release archives, `go install` and what the installers check:
[Install](https://aval.run/main/install/).

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

<!-- --8<-- [start:key] (the docs guides include this block) -->
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
<!-- --8<-- [end:key] -->

Run the test, then sign and push the result. The output says `miss`:
this run has no policy, so it does not look for a result and runs the test:

<!-- --8<-- [start:push] -->
```sh
aval run --push --key aval.key --registry localhost:5001/demo/hello-aval
```
<!-- --8<-- [end:push] -->

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
`$SIGSTORE_ID_TOKEN` (on GitHub Actions, request `id-token: write`). The
policy then trusts the CI identity instead of a key file; see the
[policy file](https://aval.run/main/trust-model/#policy-file) and
[registry credentials](https://aval.run/main/artifact-format/#registry-credentials).

```sh
aval run --push --registry ghcr.io/acme/orders-aval --policy policy.yaml
```

Every exit code is in the [CLI reference](https://aval.run/main/cli-reference/#exit-codes).

## Status: pre-alpha

Formats are frozen at `aval.trianalab.io/v1` (contract and verify report) and
predicate `execution/v2`; see the
[compatibility rules](https://aval.run/main/artifact-format/#versioning-and-compatibility)
and the [roadmap](https://aval.run/main/vision/#roadmap).

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md), [SECURITY.md](SECURITY.md) and the
[Go + Compose example](examples/go-compose).

## License

Apache-2.0. See [LICENSE](LICENSE).
