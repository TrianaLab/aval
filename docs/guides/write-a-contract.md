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
   `scope.dependencies` lists lockfiles. Globs are relative to the contract's
   directory; `**` matches any number of directories.
4. **Pick a cacheability class.** It tells Aval and the policy how safe reuse
   is.

| Class | Use it when | Reused |
|---|---|---|
| `hermetic` | the test reads only the declared files | yes |
| `controlled-environment` | it uses services you pin, such as a Compose image by digest | yes |
| `external-dependencies` | it calls something you do not control | only if the policy allows it |
| `non-deterministic` | the same inputs can give a different result | never |

## Check the scope

`--explain` prints every file, toolchain entry and variable that went into the
fingerprint. A file that is not listed is not an input: changing it does not
change the fingerprint. If the test reads it, add it to `scope`.

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
    `environment.toolchain` records versions, such as `go: "1.25"`. Aval does
    not check them. `environment.env` names variables whose values go into the
    fingerprint.

## Read next

- [Write a trust policy](trust-policy.md)
- [The test contract](../contract.md): every field and rule
