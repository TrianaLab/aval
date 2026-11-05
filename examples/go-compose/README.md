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
# sha256:0be6777f...
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

Plain `aval run` without `--fingerprint-only` prints the fingerprint and then
exits 6 (not implemented): lookup, execution and publishing land in milestone 2.

## Target UX (milestone 2+, not implemented)

```sh
# CI runner with a verifiable identity: run once, sign, publish.
aval run -f aval.yaml --registry ghcr.io/acme/counter-aval --push

# Anywhere else with the same inputs: verify and reuse instead of running.
aval run -f aval.yaml --registry ghcr.io/acme/counter-aval --policy policy.yaml
aval verify -f aval.yaml --registry ghcr.io/acme/counter-aval --output json
```

`aval run` would look up `sha256-<fingerprint>` in the repository, verify the
signed attestation against the trust policy and reuse it (exit 0), or run
`runner.command` on a miss. `aval verify` exits 0 when verified, 4 on a miss
and 5 when an attestation exists but fails signature or policy checks. With no
policy, nothing is trusted and the test always runs.

## Notes

- `cacheability: controlled-environment` is only honest if the service image is
  pinned. `compose.yaml` pins by tag for readability; pin by digest for real
  use, otherwise a re-pushed tag changes the test environment without changing
  the fingerprint.
- `environment.toolchain` is a declaration: v0 does not check the installed Go
  version.
