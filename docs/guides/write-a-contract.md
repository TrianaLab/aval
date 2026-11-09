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
aval explain
```

## Step by step

1. **Name the test.** `metadata.name` says what is checked. It stays the same
   when the inputs change.
2. **Say how to run it.** `runner.command` is an argv list. Aval does not use
   a shell unless you call one, like the example does.
3. **List the inputs.** `scope.source` lists the files the test reads,
   `scope.dependencies` the manifests and lockfiles (`go.mod`, `go.sum`).
   Globs are relative to the contract; `**` matches any number of
   directories. A glob that matches nothing means no fingerprint, so the
   test always runs (`exclude` globs may match nothing).
4. **Pick a cacheability class.** It tells Aval and the policy how safe reuse
   is. Tags, ranges and branches block reuse for `hermetic` and
   `controlled-environment`: [pin them](../pinning.md).

--8<-- "docs/contract.md:cacheability"

## Check the scope

`--explain` lists every input. Not listed means not hashed: add what is missing.

??? example "What `--explain` prints"
    One line per input: kind, sha256, `x` for an executable file, path.

    ```text
    --8<-- "internal/fingerprint/testdata/golden/aval-fingerprint-v1.manifest:1:26"
    ```

??? info "Environment"
    `environment.toolchain` values (`go: "1.25"`) and the values of the
    variables `environment.env` names go into the fingerprint as written.
    [`environment.probe`](../contract.md#probe-the-toolchain) (`go: [go,
    version]`) checks what is installed: without `1.25` in its output,
    `aval run` runs the test and publishes nothing.

## Restore coverage and reports on reuse

A reused result writes no coverage or junit file. List the files later steps
read under `outputs` (top level), e.g. `outputs: [coverage.xml, junit.xml]`:

- `aval run --push` and `aval push` publish them with the result, signed by
  digest. `aval push` reads them from disk: push right after `aval run`.
- `aval run` writes them back on reuse, after the result verifies.
  `aval verify` checks them and writes nothing.
- Keep them out of `scope`; [caps and rules](../contract.md) apply.

!!! warning "Outputs are published"
    Anyone who can read the registry repository can read them. On reuse Aval
    writes any file a trusted signer published under the globs, except this
    contract's inputs and file. List no secrets and keep the globs narrow:
    `**` can overwrite files later steps or other contracts read.

## Find undeclared inputs

A file the test reads but `scope` does not list is a false hit waiting to
happen; `--shadow` catches it only after it flips a result
([Measure reuse](measure-reuse.md)). Compare the files the test opens with
`--explain`. Here pytest loads `tests/conftest.py`, outside the contract's
`src/**` and `tests/unit/**`. On Linux, from the contract's directory:

```sh
strace -f -e trace=openat -o trace.txt pytest tests/unit
grep -v ENOENT trace.txt | grep -o '"[^"]*"' | tr -d '"' | sed "s|^$PWD/||" \
  | grep -v '^/' | sort -u > read.txt
aval explain | awk '$1 == "source" || $1 == "dependency"' \
  | cut -d' ' -f4- | sort -u > scope.txt
comm -23 read.txt scope.txt   # prints tests/conftest.py
```

Add it to `scope.source`. Read the list, do not paste it: some lines are
caches (`__pycache__`). Check `scope.contracts` the same way.

??? info "macOS, Windows, or without a tracer"
    - macOS: `sudo fs_usage -w -f pathname <process name>` while it runs.
    - Windows: Process Monitor, filtered on the test's process and directory.
    - Anywhere: copy only the scope to an empty directory and run the test
      there; a missing input usually fails the run or changes its result:

    ```sh
    scope=$(mktemp -d)
    aval explain | awk '$1 == "source" || $1 == "dependency"' \
      | cut -d' ' -f4- | tar -cf - -T - | tar -xf - -C "$scope"
    (cd "$scope" && pytest tests/unit)
    ```

## Read next

- [Shards and matrices](shards-and-matrices.md): one contract per shard
- [Write a trust policy](trust-policy.md)
- [Pin your dependencies](../pinning.md)
- [The test contract](../contract.md): every field and rule
