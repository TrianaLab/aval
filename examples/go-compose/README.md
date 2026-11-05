# Example: Go + docker compose

A tiny Redis client (`counter/`, standard library only) with a fast unit test
and an integration test (build tag `integration`) that needs the Redis service
from `compose.yaml`. `aval.yaml` is the test contract for the integration test.

This is its own Go module, so the root `go build ./...` ignores it.

## Plain Go

```sh
go test ./...                    # unit test only
go test -tags=integration ./...  # integration test skips: REDIS_ADDR unset
docker compose up -d --wait
REDIS_ADDR=localhost:16379 go test -tags=integration ./...
docker compose down
```

With `REDIS_ADDR` set, an unreachable Redis fails the test instead of skipping
it, so a run without the service can never be recorded as a pass.

## What works today (milestone 1)

From the repo root:

```sh
go build -o bin/aval ./cmd/aval
bin/aval run --fingerprint-only -f examples/go-compose/aval.yaml
# sha256:<64 hex>
bin/aval run --explain --fingerprint-only -f examples/go-compose/aval.yaml
```

`--explain` prints the manifest that was hashed: the contract, every file
matched by `scope.source` (`**/*.go`, `compose.yaml`), `go.mod` as a
dependency, and the declared toolchain. Anything else in the directory is not
an input.

Editing this README does not change the fingerprint; editing a `.go` file does:

```sh
fp() { bin/aval run --fingerprint-only -f examples/go-compose/aval.yaml; }
a=$(fp); echo >> examples/go-compose/README.md;     b=$(fp); [ "$a" = "$b" ] && echo "README: same"
echo >> examples/go-compose/counter/counter.go;     c=$(fp); [ "$b" != "$c" ] && echo ".go: changed"
```

(Revert both edits afterwards.) The same applies to `compose.yaml`, `go.mod`,
`aval.yaml` itself (including `runner.command`) and the executable bit of any
scoped file.

Plain `aval run` without a registry prints the fingerprint, reports a miss
and runs `runner.command`.

## Reuse through a registry

```sh
# CI runner with a verifiable identity: run once, sign, publish.
aval run -f aval.yaml --registry ghcr.io/acme/counter-aval --push

# Anywhere else with the same inputs: verify and reuse instead of running.
aval run -f aval.yaml --registry ghcr.io/acme/counter-aval --policy policy.yaml
aval verify -f aval.yaml --registry ghcr.io/acme/counter-aval --output json
```

`aval run` looks up `sha256-<fingerprint>` in the repository, verifies the
signed attestation against the trust policy and reuses it (exit 0 for a
passing result, 3 for a failing one), or runs `runner.command` on a miss.
A verification failure runs the test and exits 5. `aval verify` exits 0 when verified, 4 on a miss
and 5 when an attestation exists but fails signature or policy checks. With no
policy, nothing is trusted and the test always runs.

## GitHub Actions demo

The `example-go-compose` job in
[`.github/workflows/ci.yaml`](../../.github/workflows/ci.yaml) runs this
example on every push to `main` against `ghcr.io/trianalab/aval-example-go-compose`,
signing keyless with the workflow's OIDC token. [`policy.yaml`](policy.yaml)
trusts only that workflow on `main`, at runner level. The job:

1. **Publish.** `aval run --push`: on a miss it runs the test, signs the
   result and publishes it; if an earlier run already published this
   fingerprint, it reuses that result instead.
2. **Hit.** `aval run` with the scope unchanged: outcome `verified`, the test
   does not run.
3. **Miss.** Appends a comment to `counter/counter.go` (unique per run) and
   runs `aval run`: outcome `miss`, the test runs.
4. **Untrusted attestation.** For another fixed change to `counter.go`, a
   result signed with a throwaway key is published once, as anyone with write
   access to the repository could. `aval verify` reports
   `verification-failed` and exits 5. The job expects that exit code and shows
   it as a warning annotation.

Each outcome goes to the job summary, and the verify reports are uploaded as
the `example-go-compose-reports` artifact. The job needs `packages: write` and
`id-token: write`, so it never runs on pull requests;
`tests/acceptance/run_test.go` runs the same story against an in-process
registry with key-based signing.

## Notes

- `cacheability: controlled-environment` is only honest if the service image is
  pinned. `compose.yaml` pins by tag for readability; pin by digest for real
  use, otherwise a re-pushed tag changes the test environment without changing
  the fingerprint.
- `environment.toolchain` is a declaration: v0 does not check the installed Go
  version.
