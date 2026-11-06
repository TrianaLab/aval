# CLI reference

Generated from the cobra command tree by `make docs-cli`. Do not edit by hand:
`make ci-docs` fails when this file and the CLI disagree.

## Exit codes

<!-- --8<-- [start:exit-codes] -->

| Code | `aval run` | `aval verify` | `aval push` |
|---|---|---|---|
| 0 | the test passed, or a verified passing result was reused | a verified result exists (passing or failing) | pushed |
| 1 | error (the test still runs when it can), or a false hit in `--shadow` | error: registry, policy, trusted root or fingerprint | error: signing, registry or a result that does not match the current inputs |
| 2 | bad flags or invalid `aval.yaml` | bad flags or invalid `aval.yaml` | bad flags or invalid `aval.yaml` |
| 3 | the test failed, or a verified failing result was reused | not used | not used |
| 4 | not used: a miss runs the test | miss: no result the policy can reuse | not used |
| 5 | verification failed, after running the test: a security event | verification failed: a security event | not used |
| 6 | reserved, never returned | reserved, never returned | reserved, never returned |

A miss (4) is routine. A verification failure (5) means someone published a
result the policy does not trust; never hide it or retry it away.

<!-- --8<-- [end:exit-codes] -->

## aval

Verifiable test execution reuse: signed test results as OCI artifacts

```text
Usage:
  aval [flags]
  aval [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  push        Sign and publish a recorded execution result
  record      Publish a run record written by 'aval run --record-file'
  run         Reuse a verified result for the current inputs, or run the test
  verify      Check whether a trusted result exists for the current inputs

Flags:
  -h, --help      help for aval
  -v, --version   version for aval

Use "aval [command] --help" for more information about a command.
```

## aval completion

Generate the autocompletion script for aval for the specified shell.
See each sub-command's help for details on how to use the generated script.

```text
Usage:
  aval completion [command]

Available Commands:
  bash        Generate the autocompletion script for bash
  fish        Generate the autocompletion script for fish
  powershell  Generate the autocompletion script for powershell
  zsh         Generate the autocompletion script for zsh

Flags:
  -h, --help   help for completion

Use "aval completion [command] --help" for more information about a command.
```

## aval push

push signs the result recorded by 'aval run' and publishes it. The result
must be for the current inputs: same test id, cacheability and fingerprint.
Keyless signing reads the OIDC token from $SIGSTORE_ID_TOKEN.

```text
Usage:
  aval push [flags]

Flags:
  -f, --contract string             path to the test contract (default "aval.yaml")
  -h, --help                        help for push
      --key string                  signing key path (default: keyless OIDC)
      --registry string             OCI repository for attestations (required)
      --registry-timeout duration   deadline for this command's registry calls (lookup and fetch share one; push has its own) (default 1m0s)
      --result string               result recorded by 'aval run' (default ".aval/result.json")
```

## aval record

record publishes a run record that 'aval run --record-file' wrote to the
test's stats anchor in the registry, as 'aval run --record' would have. Use it
to publish, from a job with write credentials that runs no tests, the record
of a run made without them. Run records are unsigned statistics: run and
verify never read them.

```text
Usage:
  aval record [flags]

Flags:
  -h, --help                        help for record
      --record-file string          run record written by 'aval run --record-file' (required)
      --registry string             OCI repository for run records (required)
      --registry-timeout duration   deadline for this command's registry calls (lookup and fetch share one; push has its own) (default 1m0s)
```

## aval run

run computes the execution fingerprint of the contract, looks for a signed
attestation in the registry, and reuses it if it verifies against the trust
policy. Otherwise it runs runner.command, records the result (--result) and,
with --push, signs and publishes it.

Anything that cannot be proven runs the test: a fingerprint error, a registry
or policy error, a miss. A verification failure also runs the test, and then
exits 5: it is a security event. A reused failing result exits 3, like a
failing run. --shadow verifies and still runs the test; if the verified
result disagrees with the run, that is a false hit and run exits 1.

--record publishes a run record (outcome, reuse, durations) for statistics
to the test's stats anchor in the registry, for every outcome. It is
best-effort: a failure, such as missing write credentials, is a warning and
never changes the exit code. Records are unsigned and never read by run or
verify. --record-file writes the same record to a file for 'aval record'.

Exit codes: 0 passed or reused a pass, 3 failed or reused a failure,
5 verification failed (after running the test), 1 error or false hit,
2 usage. Keyless signing reads the OIDC token from $SIGSTORE_ID_TOKEN;
runner.command never sees it.

```text
Usage:
  aval run [flags]

Flags:
      --ci-run-url string           link to the CI run, stored in the run record
  -f, --contract string             path to the test contract (default "aval.yaml")
      --explain                     print the fingerprint manifest (what was hashed)
      --fingerprint-only            print the execution fingerprint and exit
  -h, --help                        help for run
      --key string                  signing key path for --push (default: keyless OIDC)
      --no-reuse                    always run, never reuse
      --policy string               trust policy file (required for reuse; without it the test always runs)
      --push                        sign and publish the result after running (needs --registry)
      --record                      publish a run record for statistics after the run (needs --registry; best-effort)
      --record-file string          also write the run record to this file, for 'aval record'
      --registry string             OCI repository for attestations, e.g. ghcr.io/acme/orders-aval
      --registry-timeout duration   deadline for this command's registry calls (lookup and fetch share one; push has its own) (default 1m0s)
      --report string               also write the lookup decision as a verify report (JSON) to this file
      --result string               where to record the result for 'aval push' (default ".aval/result.json")
      --shadow                      verify, but run the test anyway and fail on a false hit
```

## aval verify

verify computes the fingerprint, looks up the attestation and verifies its
signature and the trust policy, without running anything.

Exit codes: 0 verified, 4 miss, 5 verification failed (security event),
1 error (registry, policy or fingerprint), 2 usage or invalid aval.yaml.

```text
Usage:
  aval verify [flags]

Flags:
  -f, --contract string             path to the test contract (default "aval.yaml")
  -h, --help                        help for verify
  -o, --output string               output format: text or json (default "text")
      --policy string               trust policy file (without it every lookup is a miss)
      --registry string             OCI repository for attestations (required)
      --registry-timeout duration   deadline for this command's registry calls (lookup and fetch share one; push has its own) (default 1m0s)
```
