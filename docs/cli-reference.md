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
  -h, --help   help for aval

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

Sign and publish a recorded execution result

```text
Usage:
  aval push [flags]

Flags:
  -f, --contract string   path to the test contract (default "aval.yaml")
  -h, --help              help for push
      --key string        signing key path or KMS URI (default: keyless OIDC)
      --registry string   OCI repository for attestations (required)
      --result string     result recorded by 'aval run' (default ".aval/result.json")
```

## aval run

run computes the execution fingerprint of the contract, looks for a signed
attestation in the registry, and reuses it if it verifies against the trust
policy. Otherwise it runs the test (and, with --push, publishes the result).

```text
Usage:
  aval run [flags]

Flags:
  -f, --contract string    path to the test contract (default "aval.yaml")
      --explain            print the fingerprint manifest (what was hashed)
      --fingerprint-only   print the execution fingerprint and exit
  -h, --help               help for run
      --no-reuse           always run, never reuse
      --policy string      trust policy file (required for reuse; without it the test always runs)
      --push               sign and publish the result after running
      --registry string    OCI repository for attestations, e.g. ghcr.io/acme/orders-aval
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
