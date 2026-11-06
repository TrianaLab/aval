# Troubleshooting

Goal: go from an exit code or an error message to its cause and its fix.

```mermaid
flowchart LR
  code{exit code} -- 2 --> u([fix the command or aval.yaml])
  code -- 1 --> e([read the aval: line]):::warn
  code -- 4 --> m([miss: routine]):::muted
  code -- 5 --> s([security event: look]):::err
```

## Start with the exit code

```sh
aval verify --registry ghcr.io/acme/orders-aval --policy policy.yaml; echo "exit $?"
```

| Code | Cause | Fix |
|---|---|---|
| 0 | passed, or a verified result was reused | nothing |
| 1 | an error (`aval run` still runs the test), or `FALSE HIT` with `--shadow` | read the `aval:` line below |
| 2 | bad flags or an invalid `aval.yaml` | fix the flag or field the message names |
| 3 | the test failed, or `aval run` reused a failing result | fix the test |
| 4 | `aval verify` found nothing it may reuse | nothing: run the test |
| 5 | a result does not verify: a security event | see [Verification failures](#verification-failures-exit-5); never retry it away |
| 6 | reserved, never returned | nothing |

Find the message in a section below. Text in `<...>` changes per run.

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
    | `holds neither an Aval result nor a report`, `is an input of the test` | write `--result` and `--report` under `.aval/` |

??? failure "Contract errors"
    | Message | Fix |
    |---|---|
    | `or pass its path with -f` | create `aval.yaml`, or pass `-f path/to/aval.yaml` |
    | `apiVersion: got <x>, want <y>`, `that is the pre-freeze version`, `kind: got <x>, want <y>` | set `apiVersion: aval.trianalab.io/v1`, `kind: Test` |
    | `decoding contract: <x>`, `multiple YAML documents are not allowed` | fix the YAML or the misspelt field; keep one document |
    | `metadata.name: <x> must be 1-64 chars` | lower case letters, digits, `.`, `_`, `-` |
    | `runner.command: must be a non-empty argv list` | `command: ["go", "test", "./..."]` |
    | `scope.source: at least one glob is required` | list the files the test reads |
    | `must be a relative, slash-separated path`, `empty, '.' or '..' segments are not allowed`, `'**' must be a whole path segment` | write globs like `src/**/*.go` |
    | `cacheability: required`, `cacheability: unknown class` | pick a [class](write-a-contract.md#step-by-step) |
    | `environment.toolchain: invalid key`, `invalid variable name` | lower case tool names, valid variable names |

## Errors (exit 1)

`aval run` still runs the test when it can.

??? failure "Fingerprint and trust policy"
    | Message | Fix |
    |---|---|
    | `scope.source matched no files`, `glob <x> matched no files` | fix the glob, or create the file |
    | `is excluded only by a trailing` | exclude the file by name |
    | `only regular files are supported in scope`, `control characters in path are not supported` | keep symlinks and odd names out of scope |
    | `decoding policy: <x>`, `minTrustLevel: <x> must be developer, runner or isolated`, `identities: at least one is required` | fix the field ([Write a trust policy](trust-policy.md)) |
    | `publicKey cannot be combined with issuer or subjectRegexp`, `issuer and subjectRegexp are required (or publicKey)` | one kind of signer per identity |
    | `not a PEM public key`, `larger than <n> bytes`, `subjectRegexp: <x>` | fix the key file or the regexp |
    | `allowedCacheability: non-deterministic results are never reusable`, `allowedCacheability: unknown class` | remove that class |
    | `sigstore trusted root: <x>` | allow network and a writable `$HOME`, or pin `trustedRoot` |

??? failure "Registry, push and record"
    | Message | Fix |
    |---|---|
    | `loading registry credentials: <x>`, `resolving <tag>: <x>`, `listing referrers: <x>`, `fetching manifest <x>`, `fetching layer <x>` | check the address, the network and `docker login` |
    | `signing: <x>`, `publishing: <x>`, `pushing manifest: <x>`, `tagging anchor: <x>` | check the token or key, and write access |
    | `more than <n> attestations for fingerprint`, `is not listed among the referrers of` | delete old results ([cleanup](../artifact-format.md#the-64-candidate-cap-and-cleanup)) |
    | `every listed attestation is gone`, `is gone, skipping it` | someone deleted results during the lookup: run again |
    | `running <x>` | install the program `runner.command` starts |
    | `writing report: <x>`, `result not published: <x>` | fix the `--report` path, or the fingerprint error before it |
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
    | `not reusable under the trust policy` | the signer is below `minTrustLevel`; raise its `trustLevel` if you trust it |

## Verification failures (exit 5)

`aval: SECURITY: <n> attestation(s) for <test> failed signature or policy verification`
comes first. The `reason:` line says why. A wrong policy is the common cause;
an attacker is the dangerous one. Fail the job and look.

??? danger "Reasons"
    | Reason | Fix |
    |---|---|
    | `signer key id <x> is not in the trust policy`, `it lists no keyless identities`, `is not accepted by the policy` | trust that signer, or find out who pushed |
    | `bundle signs a different envelope`, `no Sigstore bundle stored with the attestation`, `decoding Sigstore bundle`, `bundle does not carry a DSSE envelope`, `envelope: missing or not an in-toto payload`, `decoding statement`, `statement: type <x>`, `predicate: <x>`, `malformed attestation: <x>` | a broken or forged result: delete it |
    | `fingerprint <x>, want <y>`, `test id <x>, want <y>`, `statement subject does not match the predicate subject` | a result that claims other inputs: delete it |
    | `cacheability <x> is not reusable under the policy`, `tag <x> points at <y>, not the fingerprint anchor` | a lie about the result or the tag: find out who pushed |

## Read next

- [Outcomes](../concepts/outcomes.md)
- [CLI reference](../cli-reference.md#exit-codes)
