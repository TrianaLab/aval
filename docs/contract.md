# The test contract: `aval.yaml`

`aval.yaml` says what a test is, how to run it and which files it reads. Aval
reuses a result only when those declared inputs are identical. An input you
do not declare is invisible to Aval. Format: `aval.trianalab.io/v1`, frozen
([Versioning](#versioning)).

## Example

<!-- --8<-- [start:example] -->

```yaml
apiVersion: aval.trianalab.io/v1   # required, exact value
kind: Test                      # required, exact value

metadata:
  name: orders-integration      # test identity: WHAT is checked

runner:
  # argv, executed without a shell. Order matters and is part of the fingerprint.
  command: ["go", "test", "-tags=integration", "./orders/..."]

scope:
  source:                       # required, each glob must match >= 1 file
    - "orders/**/*.go"
    - "internal/**/*.go"
    - "testdata/orders/**"
  exclude:                      # optional, removes files selected by source
    - "**/*_bench_test.go"
  dependencies:                 # optional, each glob must match >= 1 file
    - "go.mod"
    - "go.sum"
  allowMutable:                 # optional, accepts a mutable reference on purpose
    - ref: "postgres:16"
      reason: "rebuilt and scanned nightly"

environment:
  toolchain:                    # optional, declared, not detected (v0)
    go: "1.25"
  env:                          # optional, names only; values are hashed
    - CGO_ENABLED
    - GOFLAGS

cacheability: hermetic          # required, see "Cacheability classes"
```

<!-- --8<-- [end:example] -->

Check it locally:

```sh
aval run -f path/to/aval.yaml --fingerprint-only   # prints sha256:<hex>
aval run -f path/to/aval.yaml --explain --fingerprint-only   # also prints the manifest
```

Diff the `--explain` output of two machines to see which file, toolchain entry
or variable differs. `-f` takes one path and defaults to `./aval.yaml`; the
root is always the contract file's directory. A file holds one contract: give
each shard of a split suite its own file
([Shards and matrices](guides/shards-and-matrices.md)).

## Fields

- Unknown fields and files with more than one YAML document are rejected
  (exit 2), so a typo like `sorce:` fails instead of shrinking the scope.
- Every field is part of the fingerprint.

| Field | Type | Required | Rules |
|---|---|---|---|
| `apiVersion` | string | yes | Must be `aval.trianalab.io/v1`. |
| `kind` | string | yes | Must be `Test`. |
| `metadata.name` | string | yes | 1-64 chars of `[a-z0-9._-]`, starting and ending with `[a-z0-9]`. |
| `runner.command` | list of strings | yes | Non-empty argv, first element not blank. No shell. |
| `scope.source` | list of globs | yes | At least one glob. Each glob must match a file, and at least one file must remain after `exclude`. |
| `scope.exclude` | list of globs | no | Removes files from the `source` set (not from `dependencies`). May match nothing. |
| `scope.dependencies` | list of globs | no | Lockfiles and manifests. Each glob must match a file. |
| `scope.allowMutable` | list of `{file, ref, reason}` | no | Accepts a mutable reference on purpose: `ref` exactly as Aval reports it, plus a non-blank `reason`. An optional `file` (relative to the contract, or `runner.command`) limits it to that file; without `file`, a `ref` that several files report is not accepted. See [Pin your dependencies](pinning.md#accept-a-reference-on-purpose). |
| `scope.resolve` | list of `kind: reference` | no | One `kind: reference` per entry. Kind is one of `image`, `npm`, `pypi`, `go`, `maven`, `crate`, `gem`, `git`, `url`. The reference must not contain credentials. See [Resolve references](resolve/index.md). |
| `scope.contracts` | list of paths | no | Other contracts this test depends on ([Depend on another part of the repo](guides/depend-on-a-contract.md)). Each path is relative to this contract's directory: clean (`..` only at the start), slash-separated, no spaces, ending in `.yaml` or `.yml`, listed once. At fingerprint time the file must be a valid contract inside the git working tree of this contract (the nearest directory at or above it with a `.git` entry), with no symlink on the path; if this contract's own directory is a symlink, `..` is resolved from its real location. No cycles, at most 8 levels and 64 distinct contracts. A referenced contract's mutable references block reuse here too, unless its own `scope.allowMutable` accepts them. |
| `environment.toolchain` | map string to string | no | Keys match `^[a-z0-9][a-z0-9._-]*$`. Values are not validated. |
| `environment.env` | list of strings | no | Variable names matching `^[A-Za-z_][A-Za-z0-9_]*$`. |
| `cacheability` | string | yes | One of `hermetic`, `controlled-environment`, `external-dependencies`, `non-deterministic`. |
| `outputs` | list of globs | no | Files the test writes that a reused result restores, such as `coverage.xml` ([Restore coverage and reports on reuse](guides/write-a-contract.md#restore-coverage-and-reports-on-reuse)). Same glob rules as `scope`. When publishing, each glob must match a regular file (no symlink) that is not the contract file, outside the scope, `.git` and `.aval`; at most 256 files, 16 MiB each, 64 MiB in total. Otherwise nothing is published (exit 1). Omitted from the fingerprint when empty, so existing contracts keep theirs; adding it changes the fingerprint, so a result without outputs is never reused for it. |

## Globs

`scope.source`, `scope.exclude`, `scope.dependencies` and `outputs` use the
same globs:

| Glob | Matches |
|---|---|
| `src/**/*.go` | `src/a.go` and `src/x/y/a.go` |
| `*.go` | top-level files only: `*` does not cross `/` |
| `dir/**` | every file under `dir`; globs never match directories |
| `**` | every file |
| Rejected | `/a`, `./a`, `../a`, `a//b`, `a\b`, `foo**`, `**.go`, `[a-` |

- Relative to the contract's directory; a glob cannot escape it.
- Case-sensitive and byte-exact: `Makefile` is not `makefile`, NFC is not NFD.
- Order does not matter.

## Test identity vs execution fingerprint

`metadata.name` says what is checked. The fingerprint says with which inputs.
Renaming a test changes its fingerprint.

## Cacheability classes

<!-- --8<-- [start:cacheability] -->

| Class | Use it when | Reused |
|---|---|---|
| `hermetic` | the test reads only the declared files | yes |
| `controlled-environment` | it uses services you pin, such as a Compose image by digest | yes |
| `external-dependencies` | it calls something you do not control | only if the policy allows it |
| `non-deterministic` | the same inputs can give a different result | never |

<!-- --8<-- [end:cacheability] -->

A reusable class still needs a signed result your policy trusts. A
`non-deterministic` test is never looked up. A `hermetic` or
`controlled-environment` test with a mutable reference that `allowMutable`
does not accept is a miss, and its result is not published
([Pin your dependencies](pinning.md)).

## What goes into the fingerprint

The fingerprint is `sha256:<hex>` of a line-oriented manifest (format
`aval-fingerprint/v1`). `aval run --explain` prints the exact text that was
hashed, in this order:

```text
aval-fingerprint/v1
contract <sha256 of canonical contract JSON>
source <sha256> <mode> <path>
dependency <sha256> <mode> <path>
toolchain <key>=<value>
env <NAME> <sha256 of value | unset>
resolve <kind> <reference> <version> <immutable id>
contract <path> <name> <fingerprint>
```

| Line | Holds | Not affected by |
|---|---|---|
| header | `aval-fingerprint/v1`, bumped when an existing contract's manifest would change | |
| `contract` | sha256 of the parsed contract as JSON | comments, formatting, key order, the order and duplicates of set-like lists (argv order counts; an empty list equals an omitted one) |
| `source`, `dependency` | sha256 of the content, `x` (any exec bit) or `-`, path. A file in both sets appears twice | owner, timestamps, other permission bits |
| `toolchain` | `key=value` as written; not checked against the installed tools | |
| `env` | sha256 of the value, or `unset` (empty is not unset); values never appear in clear | |
| `resolve` | kind, reference, version or `floating`, immutable id; only with `scope.resolve`, and needs the network ([Resolve references](resolve/index.md)) | the registry address |
| `contract <path>` | one line per `scope.contracts` entry: the path as written, the referenced contract's `metadata.name` and its fingerprint, computed from its own directory and inputs (so references are transitive). `--explain` also prints the tree, one `depends on <path> (<name>) <fingerprint>` line per reference | |

Lines are sorted within each section, so checkout path and walk order do not
matter. Paths are hashed byte for byte. Windows has no exec bit, so an
executable file fingerprints differently there than on Unix.

### What does not go in

| Not hashed | Note |
|---|---|
| files no `source` or `dependencies` glob matches | the main risk: keep the scope honest |
| `.git` directories | exact name; a regular `.git` file is hashed |
| `.aval/` directories and the command's `--result`/`--report` files | so `["**"]` stays stable; keep them under `.aval/`. A path in scope that holds a real input exits 2. In a `scope.contracts` reference they stay inputs (a miss, never a false hit) |
| commit, branch, VCS metadata | two commits with the same scope share a result |
| environment variables not in `environment.env` | |
| the real toolchain, OS and architecture | unless declared |

### Known gaps

Declared scope means some changes are invisible by design. A test that
depends on one of these can reuse a result it should not:

- a file the test reads that `scope` does not match, including a case or
  Unicode mismatch under a wildcard glob
- an environment variable the test reads that `environment.env` does not list
- content behind a name the [scanner](pinning.md) does not recognize, or a
  network service: use `scope.resolve` or `external-dependencies`
- the installed toolchain, OS and architecture

Mitigation: review the contract, use `aval run --shadow`, pick the right
cacheability class.

### Fail-closed errors

Aval cannot prove the inputs: `aval run` reports outcome `error`, runs the
test and records nothing; `--fingerprint-only`, `--explain`, `aval verify` and
`aval push` exit 1.

| Message | Fix |
|---|---|
| `source glob "<glob>" matched no files` | fix the glob; a case or Unicode mismatch also matches nothing |
| `scope.source matched no files` | `exclude` removed every source file |
| `source "<path>" is excluded only by a trailing "**"` | exclude it by name, or narrow the glob |
| `dependency glob "<glob>" matched no files` | fix the glob |
| `only regular files are supported in scope` | remove the symlink, device or socket from scope |
| `"<path>" is a symlink a glob could match files under` | symlinks are not followed, and `exclude` does not lift this; the contract directory itself may be a symlink |
| `control characters in path are not supported` | rename the file (newline or carriage return) |
| `resolve <kind> <reference>: <reason>` | see [When resolution fails](resolve/index.md#when-resolution-fails) |
| `scope.contracts <path>: <reason>` | any error above in the referenced contract, or: missing or invalid contract, `leads outside the repository root`, `no git working tree (.git) at or above <dir>`, `<path> is a symlink`, `<path> is not a regular file`, `cycle: a/aval.yaml -> b/aval.yaml -> a/aval.yaml`, `more than 8 levels or 64 contracts` |
| any I/O error while walking or reading | make the file or directory readable; the walk only enters directories a glob can match |

## Versioning

`aval.trianalab.io/v1` is frozen; policy files use the same value. Within v1
the schema only gets new optional fields. An older `aval` rejects a field it
does not know, which fails closed.

`aval.dev/v1alpha1` is rejected: change that line. Rules:
[artifact format](artifact-format.md#versioning-and-compatibility).

## Future work

- **Detected toolchain**: verify or record the installed toolchain instead
  of trusting `environment.toolchain`. Not scheduled in milestones 1-5.
- **Observed inputs**: trace which files the test actually reads, to flag
  undeclared inputs or suggest scope. v0 never infers scope.
