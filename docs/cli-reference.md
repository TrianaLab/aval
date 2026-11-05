# CLI reference

Generated from the cobra command tree by `make docs-cli`. Do not edit by hand:
`make ci-docs` fails when this file and the CLI disagree.

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
  -f, --contract string   path to the test contract (default "aval.yaml")
  -h, --help              help for push
      --key string        signing key path (default: keyless OIDC)
      --registry string   OCI repository for attestations (required)
      --result string     result recorded by 'aval run' (default ".aval/result.json")
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

Exit codes: 0 passed or reused a pass, 3 failed or reused a failure,
5 verification failed (after running the test), 1 error or false hit,
2 usage. Keyless signing reads the OIDC token from $SIGSTORE_ID_TOKEN;
runner.command never sees it.

```text
Usage:
  aval run [flags]

Flags:
  -f, --contract string    path to the test contract (default "aval.yaml")
      --explain            print the fingerprint manifest (what was hashed)
      --fingerprint-only   print the execution fingerprint and exit
  -h, --help               help for run
      --key string         signing key path for --push (default: keyless OIDC)
      --no-reuse           always run, never reuse
      --policy string      trust policy file (required for reuse; without it the test always runs)
      --push               sign and publish the result after running (needs --registry)
      --registry string    OCI repository for attestations, e.g. ghcr.io/acme/orders-aval
      --report string      also write the lookup decision as a verify report (JSON) to this file
      --result string      where to record the result for 'aval push' (default ".aval/result.json")
      --shadow             verify, but run the test anyway and fail on a false hit
```

## aval verify

verify computes the fingerprint, looks up the attestation and verifies its
signature and the trust policy, without running anything.

Exit codes: 0 verified, 4 miss, 5 verification failed (security event).

```text
Usage:
  aval verify [flags]

Flags:
  -f, --contract string   path to the test contract (default "aval.yaml")
  -h, --help              help for verify
  -o, --output string     output format: text or json (default "text")
      --policy string     trust policy file (without it every lookup is a miss)
      --registry string   OCI repository for attestations (required)
```
