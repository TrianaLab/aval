# Quickstart

Go from an empty folder to a reused, verified test result in five minutes.
You will see the four things Aval does: a miss, a reuse, a changed input and a
rejected attestation.

You need `aval` ([Install](install.md), one line), Docker and `openssl`.
Copy each block into one shell, in order. The `text` block under it is what
you should see. `...` stands for a digest that is different on every run.

<!-- quickstart:start (tests/acceptance/readme_test.go runs the sh blocks and checks the text blocks) -->

## 1. Start a registry

Aval keeps results in an OCI registry. Start one on your laptop. Port 5001,
because macOS uses 5000 for AirPlay:

```sh
docker run -d --name aval-registry -p 5001:5000 registry:2
```

## 2. Make a project

One input file and a contract, `aval.yaml`. The contract names the test
command and the files it reads (`scope`). Aval hashes those files into a
fingerprint:

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

## 3. Make a key and a policy

The key signs your results. The policy says which signers you trust. Aval
accepts ECDSA P-256 keys:

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

## 4. First run: a miss

![aval run prints miss, runs the test, which passes, and pushes the signed result to localhost:5001](assets/gifs/miss.gif)

No result exists for these inputs, so the test runs. Aval signs the passing
result with `aval.key` and pushes it:

```sh
aval run --push --key aval.key --registry localhost:5001/demo/hello-aval
```

```text
test hello fingerprint sha256:fb3906f246f9255cf129e24de1256ac47b85e33cc548f3f517e94e237db0de06
miss hello sha256:fb3906f246f9255cf129e24de1256ac47b85e33cc548f3f517e94e237db0de06
reason: no trust policy configured (--policy): no attestation found for fingerprint
ran hello: passed (exit 0)
pushed localhost:5001/demo/hello-aval@sha256:...
```

## 5. Second run: a reuse

![aval verify prints verified, then aval run prints reused passed result without running the test](assets/gifs/hit.gif)

Check that a result you trust exists for these exact inputs:

```sh
aval verify --registry localhost:5001/demo/hello-aval --policy policy.yaml
echo "exit code: $?"
```

```text
verified hello sha256:fb3906f246f9255cf129e24de1256ac47b85e33cc548f3f517e94e237db0de06
exit code: 0
```

Run again. Aval finds the verified result and skips the test:

```sh
aval run --registry localhost:5001/demo/hello-aval --policy policy.yaml
```

```text
test hello fingerprint sha256:fb3906f246f9255cf129e24de1256ac47b85e33cc548f3f517e94e237db0de06
verified hello sha256:fb3906f246f9255cf129e24de1256ac47b85e33cc548f3f517e94e237db0de06
reused passed result localhost:5001/demo/hello-aval@sha256:...
```

## 6. Change an input: a miss again

![after editing src/hello.txt, aval run prints a new fingerprint, miss, and runs the test](assets/gifs/changed-input.gif)

Edit a file in `scope`. The fingerprint changes, no result matches it, and the
test runs:

```sh
echo "hello, world" > src/hello.txt
aval run --registry localhost:5001/demo/hello-aval --policy policy.yaml
```

```text
test hello fingerprint sha256:d52167b48089d3c3add62a6e81c6bfb0aa8d90baa98a69235c48bd892297170b
miss hello sha256:d52167b48089d3c3add62a6e81c6bfb0aa8d90baa98a69235c48bd892297170b
reason: registry.Lookup localhost:5001/demo/hello-aval: no attestation found for fingerprint
ran hello: passed (exit 0)
```

## 7. A tampered attestation: exit 5

![a result signed with other.key is pushed; aval run prints SECURITY, verification-failed, runs the test and exits 5](assets/gifs/tampered.gif)

Someone who can push to the registry, but has a key your policy does not
trust, publishes a passing result for the new inputs. Aval rejects it, runs
the test anyway and exits 5. Exit 5 is a security event, not a miss:

```sh
openssl genpkey -algorithm EC -pkeyopt ec_paramgen_curve:P-256 -out other.key
aval run --push --key other.key --registry localhost:5001/demo/hello-aval
aval run --registry localhost:5001/demo/hello-aval --policy policy.yaml
echo "exit code: $?"
```

```text
test hello fingerprint sha256:d52167b48089d3c3add62a6e81c6bfb0aa8d90baa98a69235c48bd892297170b
miss hello sha256:d52167b48089d3c3add62a6e81c6bfb0aa8d90baa98a69235c48bd892297170b
reason: no trust policy configured (--policy): no attestation found for fingerprint
ran hello: passed (exit 0)
pushed localhost:5001/demo/hello-aval@sha256:...
test hello fingerprint sha256:d52167b48089d3c3add62a6e81c6bfb0aa8d90baa98a69235c48bd892297170b
aval: SECURITY: rejected attestation localhost:5001/demo/hello-aval@sha256:...: attestation verification failed: signature: ...
verification-failed hello sha256:d52167b48089d3c3add62a6e81c6bfb0aa8d90baa98a69235c48bd892297170b
reason: attestation verification failed: signature: ...
rejected: localhost:5001/demo/hello-aval@sha256:... verification-failed: attestation verification failed: signature: ...
aval: SECURITY: attestation verification failed for hello: ...; running the test
ran hello: passed (exit 0)
aval: attestation verification failed: signature: ...
exit code: 5
```

<!-- quickstart:end -->

## What just happened

```text
              aval.yaml scope: src/**
                       |
                       v
   src/hello.txt --> fingerprint sha256:fb39...
                       |
                       v
   registry: is there a result tagged sha256-<fingerprint>?
        |                         |                          |
       no                        yes, signed by             yes, signed by
        |                        a key in policy.yaml       any other key
        v                         |                          |
   miss: run the test,            v                          v
   sign, push (--push)       verified: reuse,           verification-failed:
                             skip the test              run the test, exit 5
```

- The fingerprint covers only what `scope` lists. Change one byte there and
  it is a new fingerprint (step 6).
- A result is reused only if its signer is in your policy (step 5). Any other
  signer is rejected, loudly (step 7).
- When Aval cannot prove a result is safe to reuse, the test runs.

Stop the registry when you are done:

```sh
docker rm -f aval-registry
```

Next: [the test contract](contract.md) for real scopes, the
[trust model](trust-model.md) for keyless signing in CI, and the
[CLI reference](cli-reference.md#exit-codes) for every exit code.
