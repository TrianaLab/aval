# Write a contract

Goal: an `aval.yaml` that lists every input of your test, so a result is
reused only when none of them changed.

```mermaid
flowchart LR
  c([aval.yaml]) --> s[scope: files the test reads] --> fp[fingerprint]
  c --> k[cacheability: how safe reuse is] --> d{reusable?}
  fp --> d
```

## The example

This contract runs a Go integration test against Redis from Docker Compose.
It is [`examples/go-compose/aval.yaml`](https://github.com/TrianaLab/aval/blob/main/examples/go-compose/aval.yaml),
which CI runs:

```yaml
--8<-- "examples/go-compose/aval.yaml"
```

Put it next to your test and check what it hashes:

```sh
aval run --explain --fingerprint-only
```

## Step by step

1. **Name the test.** `metadata.name` says what is checked. It stays the same
   when the inputs change.
2. **Say how to run it.** `runner.command` is an argv list. Aval does not use
   a shell unless you call one, like the example does.
3. **List the inputs.** `scope.source` lists the files the test reads.
   `scope.dependencies` lists dependency manifests and lockfiles (`go.mod`,
   `go.sum`). Globs are relative to the contract's
   directory; `**` matches any number of directories.
4. **Pick a cacheability class.** It tells Aval and the policy how safe reuse
   is. Tags, ranges and branches block reuse for `hermetic` and
   `controlled-environment`: see
   [Pin your dependencies](../pinning.md).

--8<-- "docs/contract.md:cacheability"

## Check the scope

`--explain` lists every input. Not listed means not hashed: if the test reads
it, add it.

??? example "What `--explain` prints"
    One line per input: kind, sha256, `x` for an executable file, path.

    ```text
    --8<-- "internal/fingerprint/testdata/golden/aval-fingerprint-v1.manifest:1:26"
    ```

??? info "Globs that match nothing"
    Every glob in `source` and `dependencies` must match at least one file.
    Otherwise Aval cannot fingerprint, and the test always runs. `exclude`
    globs may match nothing.

??? info "Environment"
    The `environment.toolchain` values (`go: "1.25"`) go into the fingerprint
    as written; Aval does not check what is installed. `environment.env`
    names variables whose values go into the fingerprint.

## Restore coverage and reports on reuse

When Aval reuses a result the test does not run, so it writes no coverage
or junit file, and a step that reads one fails. List those files under
`outputs` (top level, next to `cacheability`), for example
`outputs: [coverage.xml, junit.xml]`:

- `aval run --push` and `aval push` publish them with the result, and the
  signature covers each file's digest.
- `aval run` writes them back on reuse, after the result verifies.
  `aval verify` checks them and writes nothing.
- They must not be inputs: keep them out of `scope`, and each glob must
  match a file after the run, or nothing is published.
- Caps: 256 files, 16 MiB each, 64 MiB in total.

!!! warning "Outputs are published"
    Anyone who can read the registry repository can read these files. Do
    not list a file that holds secrets.

Aval only refuses to restore over this contract's own inputs. Restoring into
another contract's scope is your responsibility: it changes that contract's
fingerprint.

## Read next

- [Write a trust policy](trust-policy.md)
- [Pin your dependencies](../pinning.md)
- [The test contract](../contract.md): every field and rule
