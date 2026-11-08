# Troubleshooting

Goal: go from an exit code or an error message to its cause and its fix.
In the messages below, text in `<...>` changes per run.

```mermaid
flowchart LR
  code{exit code} -- 2 --> u([Flags and files]):::warn
  code -- 1 --> e([Errors]):::warn
  code -- 4 --> m([Misses]):::muted
  code -- 5 --> s([Verification failures]):::err
```

## Start with the exit code

```sh
aval verify --registry ghcr.io/acme/orders-aval --policy policy.yaml; echo "exit $?"
```

--8<-- "docs/cli-reference.md:exit-codes"

## Flags and files (exit 2)

??? failure "Flag errors"
    | Message | Fix |
    |---|---|
    | `--push requires --registry`, `--record requires --registry`, `--registry is required`, `--registry and --record-file are required` | add the flags it names |
    | `--shadow and --no-reuse exclude each other` | pick one |
    | `--registry <x> is not an OCI repository` | use `host/name`, no `https://`, tag or digest |
    | `--registry-timeout must be positive`, `--output must be text or json`, `unknown command` | fix the value; see `aval <command> --help` |
    | `signing needs --key or, for keyless signing, an OIDC token` | pass `--key`, or set the token ([Signing](signing.md)) |
    | `not a PEM file`, `PEM type <x> is encrypted`, `not an ECDSA P-256 key` | make an unencrypted P-256 key ([Signing](signing.md)) |
    | `holds neither an Aval result nor a report`, `is an input of the test` | write `--result` and `--report` under `.aval/`; for `output <x> is an input of the test`, exclude it from `scope` |

??? failure "Contract errors"
    | Message | Fix |
    |---|---|
    | `or pass its path with -f` | create `aval.yaml`, or pass `-f path/to/aval.yaml` |
    | `apiVersion: got <x>, want <y>`, `that is the pre-freeze version`, `kind: got <x>, want <y>` | set `apiVersion: aval.trianalab.io/v1`, `kind: Test` |
    | `decoding contract: <x>`, `multiple YAML documents are not allowed` | fix the YAML or the misspelt field; keep one document |
    | `metadata.name: <x> must be 1-64 chars` | lower case letters, digits, `.`, `_`, `-` |
    | `runner.command: must be a non-empty argv list` | `command: ["go", "test", "./..."]` |
    | `scope.source: at least one glob is required` | list the files the test reads |
    | `empty glob`, `must be a relative, slash-separated path`, `empty, '.' or '..' segments are not allowed`, `'**' must be a whole path segment` | write globs like `src/**/*.go` |
    | `cacheability: required`, `cacheability: unknown class` | pick a [class](write-a-contract.md#step-by-step) |
    | `environment.toolchain: invalid key`, `invalid variable name`, `environment.probe.<key>: not a key of environment.toolchain`, `environment.probe.<key>: must be a non-empty argv list`, `environment.probe.<key>: environment.toolchain.<key> must not be empty` | lower case tool names, valid variable names; probe only declared, non-empty toolchain keys, each with an argv |
    | `scope.allowMutable[<i>]: ref and reason are both required`, `file <x> must be a slash-separated path relative to the contract` | give each entry a `ref`, a `reason` and, if needed, a `file` as the finding prints it ([Pin your dependencies](../pinning.md#accept-a-reference-on-purpose)) |
    | `scope.resolve[<i>]: <x>`, `unknown kind <x>`, `the reference must be non-empty`, `credentials in the reference are not allowed`, `a scope.resolve entry is one` | one known `kind: reference` per entry, no credentials ([Resolve references](../resolve/index.md)) |
    | `scope.contracts[<i>]: <x>`, `must be slash-separated`, `must be a clean relative path`, `must name a contract file`, `is listed twice` | a relative path to a `.yaml` contract, listed once ([Depend on another part of the repo](depend-on-a-contract.md)) |
    | `not a valid <kind> package name`, `is not a registry version, range or tag`, `want group:artifact:version` | use the registry name and a version, range or tag, not a path, file or URL ([Resolve references](../resolve/index.md#exact-or-floating)) |

## Errors (exit 1)

`aval verify` exits 1. `aval run` prints these as outcome `error`, runs the test and exits with the test's code. It exits 1 only when the test cannot start (`running <x>`), a probe fails (`environment.probe`) or the result cannot be published.

??? failure "Fingerprint and trust policy"
    | Message | Fix |
    |---|---|
    | `scope.source matched no files`, `glob <x> matched no files` | fix the glob, or create the file |
    | `is excluded only by a trailing` | exclude the file by name |
    | `only regular files are supported in scope`, `control characters in path are not supported` | keep symlinks and odd names out of scope |
    | `scope.contracts <path>: <x>` | fix the referenced contract or the path ([Depend on another part of the repo](depend-on-a-contract.md)) |
    | `resolve <kind> <ref>: <x>` | check name, version, network, credentials ([Resolve references](../resolve/index.md)) |
    | `decoding policy: <x>`, `minTrustLevel: <x> must be developer, runner or isolated`, `identities: at least one is required`, `identities[<i>]: <x>`, `trustLevel: <x> must be developer, runner or isolated` | fix the field ([Write a trust policy](trust-policy.md)) |
    | `publicKey cannot be combined with issuer or subjectRegexp`, `issuer and subjectRegexp are required (or publicKey)` | one kind of signer per identity |
    | `publicKey: <x>`, `publicKey <file>: <x>`, `not a PEM public key`, `larger than <n> bytes`, `subjectRegexp: <x>` | fix the key file or the regexp |
    | `allowedCacheability: non-deterministic results are never reusable`, `allowedCacheability: unknown class` | remove that class |
    | `sigstore trusted root: <x>` | allow network and a writable `$HOME`, or pin `trustedRoot` |

??? failure "Registry, push and record"
    | Message | Fix |
    |---|---|
    | `loading registry credentials: <x>`, `resolving <tag>: <x>`, `listing referrers: <x>`, `fetching manifest <x>`, `fetching layer <x>` | check the address, the network and `docker login` |
    | `signing: <x>`, `publishing: <x>`, `pushing manifest: <x>`, `tagging anchor: <x>` | check the token or key, and write access |
    | `more than <n> attestations` | delete old results ([cleanup](../artifact-format.md#the-64-candidate-cap-and-cleanup)); the `aval: SECURITY:` line before it means none was verified, so check who pushed them: it may be a flooding attempt |
    | `is not listed among the referrers of` | push again, or use a registry with OCI 1.1 referrers |
    | `running <x>`, `environment.probe <key> <argv>: declared <x>, observed <y>` | install the program `runner.command` starts, or the declared toolchain version ([Probe the toolchain](../contract.md#probe-the-toolchain)) |
    | `FALSE HIT: <x>` (with `--shadow`) | the test reads an input `scope` does not list: [find it](write-a-contract.md#find-undeclared-inputs) |
    | `writing report: <x>`, `result not published: <x>` | fix the `--report` path, or the fingerprint error before it |
    | `publishing outputs: <x>`, `more than <n> output files`, `outputs are capped at`, `is not a regular file (symlinks are not published)`, `backslashes and control characters in a path`, `is the contract`, `restoring outputs: <x>` | keep each output a regular file under the caps and narrow the globs; for `restoring outputs`, remove the symlink or directory in the way. The test ran instead ([Restore coverage and reports on reuse](write-a-contract.md#restore-coverage-and-reports-on-reuse)) |
    | `reading the result recorded by 'aval run'` | run `aval run` first |
    | `recorded result is for test <x>` | an input changed: run the test again |
    | `reading run record: <x>`, `run record <file>: <x>` | pass the file that `--record-file` wrote |
    | `warning: run record not` | a warning only; check write access |

## Misses (exit 4)

??? info "Why nothing was reused"
    | Reason | Fix |
    |---|---|
    | `no attestation found for fingerprint` | none: the inputs changed or nobody pushed yet |
    | `no trust policy configured`, `no registry configured`, `reuse disabled (--no-reuse)` | pass `--policy`, `--registry`, drop `--no-reuse` |
    | `cacheability <x> is never reusable`, `is not in allowedCacheability` | by design, or allow the class in the policy |
    | `not reusable under the trust policy`, `signer <x> has trust level <y>, policy requires at least <z>` | raise the signer's `trustLevel` if you trust it |
    | `mutable references in the inputs` | pin each reference the line lists, or acknowledge it with `scope.allowMutable`; for `lists this ref without file:`, add `file:` ([Pin your dependencies](../pinning.md#accept-a-reference-on-purpose)) |
    | `every listed attestation is gone`, `is gone, skipping it` | someone deleted results during the lookup: run again |

## Verification failures (exit 5)

The `aval: SECURITY:` line comes first and `reason:` says why. Usually a wrong
policy; possibly an attacker. See
[Outcomes](../concepts/outcomes.md#a-verification-failure-is-a-security-event).

It also appears beside a verified result (exit 0), listed in `rejected`: gate
on `rejected` where that must stop a pipeline (`.github/aval/summary.sh`).

??? danger "Reasons"
    | Reason | Fix |
    |---|---|
    | `signature: <x>`, `signer key id <x> is not in the trust policy`, `it lists no keyless identities`, `is not accepted by the policy` | trust that signer, or find out who pushed |
    | `bundle signs a different envelope`, `no Sigstore bundle stored with the attestation`, `decoding Sigstore bundle`, `bundle does not carry a DSSE envelope`, `envelope: missing or not an in-toto payload`, `decoding statement`, `statement: type <x>`, `predicate: <x>`, `malformed attestation: <x>` | a broken or forged result: delete it |
    | `fingerprint <x>, want <y>`, `test id <x>, want <y>`, `statement subject does not match the predicate subject` | a result that claims other inputs: delete it |
    | `cacheability <x> is not reusable under the policy`, `cacheability <x>, want <y>`, `tag <x> points at <y>, not the fingerprint anchor` | a lie about the result or the tag: find out who pushed |
    | `outputs: <x>`, `the result carries no outputs, the contract declares outputs`, `is not a declared output of the contract, or is an input`, `matches no output of the result` | the stored outputs do not match the signed list or the contract: a forged result, or the registry deleted the outputs. Find out who pushed, delete the result and push again |
    | `no signature verifier configured`, `expected fingerprint and test id are required` | only from Go: fill in `VerifyOptions` ([Consume from Go](go-consumer.md)) |

## Read next

- [Outcomes](../concepts/outcomes.md)
- [Pin your dependencies](../pinning.md)
