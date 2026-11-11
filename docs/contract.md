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
  toolchain:                    # optional, declared
    go: "1.25"
  probe:                        # optional, checks a toolchain entry before the run
    go: [go, version]
  env:                          # optional, names or PREFIX_* patterns; values are hashed
    - CGO_ENABLED
    - GOFLAGS

cacheability: hermetic          # required, see "Cacheability classes"
```

<!-- --8<-- [end:example] -->

Check it locally:

```sh
aval explain -f path/to/aval.yaml --digest   # prints sha256:<hex>
aval explain -f path/to/aval.yaml            # the manifest, then the fingerprint
```

It excludes only the default `.aval/result.json`; a run that writes
`--result-file`, `--report-file` or `--stats-file` inside the scope
fingerprints the same only when those paths are under `.aval/`.

Diff the `aval explain` output of two machines to see which file, toolchain entry
or variable differs. `-f` takes one path and defaults to `./aval.yaml`; the
root is the contract file's directory, or the directory `scope.root` names
above it ([Contracts in a subdirectory](#contracts-in-a-subdirectory)). A
`kind: Test` file holds one test; a `kind: TestSet` file holds many
([Many tests in one file](#many-tests-in-one-file-testset)).

## Fields

- Unknown fields and files with more than one YAML document are rejected
  (exit 2), so a typo like `sorce:` fails instead of shrinking the scope.
- Every field is part of the fingerprint.

| Field | Type | Required | Rules |
|---|---|---|---|
| `apiVersion` | string | yes | Must be `aval.trianalab.io/v1`. |
| `kind` | string | yes | `Test`, or `TestSet` for [many tests in one file](#many-tests-in-one-file-testset). |
| `metadata.name` | string | yes | 1-64 chars of `[a-z0-9._-]`, starting and ending with `[a-z0-9]`. |
| `runner.command` | list of strings | yes | Non-empty argv, first element not blank. No shell. It must not start `aval run` on the same contract again, directly or through other contracts: see [Nested runs](#nested-runs). |
| `scope.source` | list of globs | yes | At least one glob. Each glob must match a file, and at least one file must remain after `exclude`. |
| `scope.optionalSource` | list of globs | no | Hashed like `source`, but a glob may match nothing yet. A glob may not also be in `source`. See [Optional source globs](#optional-source-globs). |
| `scope.exclude` | list of globs | no | Removes files from the `source` and `optionalSource` set (not from `dependencies`). May match nothing. |
| `scope.dependencies` | list of globs | no | Lockfiles and manifests. Each glob must match a file. |
| `scope.allowMutable` | list of `{file, files, ref, reason}` | no | Accepts a mutable reference on purpose: `ref` exactly as Aval reports it, plus a non-blank `reason`. An optional `file` (an exact path relative to the contract root, or `runner.command`) limits it to that file; without `file` or `files`, a `ref` that several files report is not accepted. An optional `files`, a list of globs relative to the contract root that excludes `file`, accepts the `ref` in every file in scope that a glob matches, never in `runner.command`; each glob must match a file in scope, otherwise the contract is invalid (exit 2). See [Pin your dependencies](pinning.md#accept-a-reference-on-purpose). |
| `scope.resolve` | list of `kind: reference` | no | One `kind: reference` per entry. Kind is one of `image`, `npm`, `pypi`, `go`, `maven`, `crate`, `gem`, `git`, `url`. The reference must not contain credentials. See [Resolve references](resolve/index.md). |
| `scope.contracts` | list of paths | no | Other contracts this test depends on ([Depend on another part of the repo](guides/depend-on-a-contract.md)). Each path is relative to this contract's directory: clean (`..` only at the start), slash-separated, no spaces, ending in `.yaml` or `.yml`, listed once. At fingerprint time the file must be a valid contract inside the git working tree of this contract (the nearest directory at or above it with a `.git` entry), with no symlink on the path; if this contract's own directory is a symlink, `..` is resolved from its real location. No cycles, at most 8 levels and 64 distinct contracts. A referenced contract's mutable references block reuse here too, unless its own `scope.allowMutable` accepts them. |
| `scope.root` | string | no | The path from this contract's directory up to the root of its tree: `..` segments only (`..`, `../..`). Globs, `outputs`, `allowMutable[].file` and `files`, `runner.command` and `environment.probe` resolve from it; `scope.contracts` does not. The root must lie inside the git working tree of this contract, as for `scope.contracts`; otherwise `aval run`, `explain`, `verify` and `push` exit 2. See [Contracts in a subdirectory](#contracts-in-a-subdirectory). |
| `environment.toolchain` | map string to string | no | Keys match `^[a-z0-9][a-z0-9._-]*$`. Values are not validated. |
| `environment.probe` | map string to list of strings | no | Each key must be an `environment.toolchain` key with a non-empty value; each value a non-empty argv, first element not blank. See [Probe the toolchain](#probe-the-toolchain). |
| `environment.env` | list of strings | no | Variable names matching `^[A-Za-z_][A-Za-z0-9_]*$`, or prefix patterns: a name followed by `*` (`APP_*`), see [Environment variable patterns](#environment-variable-patterns). `AVAL_RUN_STACK` is rejected in any case (set by `aval run`, not an input), and so is a pattern that would match it (`AVAL_*`, `AVAL_RUN_*`). |
| `cacheability` | string | yes | One of `hermetic`, `controlled-environment`, `external-dependencies`, `non-deterministic`. |
| `outputs` | list of globs | no | Files the test writes that a reused result restores, such as `coverage.xml` ([Restore coverage and reports on reuse](guides/write-a-contract.md#restore-coverage-and-reports-on-reuse)). Same glob rules as `scope`. When publishing, each glob must match a regular file (no symlink) that is not the contract file, outside the scope, `.git` and `.aval`; at most 256 files, 16 MiB each, 64 MiB in total. Otherwise nothing is published (exit 1). To push from another job, `aval run --outputs-dir` copies them and `aval push --outputs-dir` publishes them from that copy ([the artifact](guides/github-actions.md#split-credentials)). Omitted from the fingerprint when empty, so existing contracts keep theirs; adding it changes the fingerprint, so a result without outputs is never reused for it. |

## Contracts in a subdirectory

A contract can live below the root of the tree it covers, such as
`aval/unit.yaml` in a monorepo. `scope.root` says how far up the root is:

```yaml
# aval/unit.yaml
scope:
  root: ..
  source: ["src/**"]
  dependencies: ["go.mod"]
```

The contract then behaves as if its file sat in the root:

- Globs, `outputs`, `allowMutable[].file` and `files`, manifest paths, finding paths
  and restored output paths are relative to the root. The manifest lists the
  same `source` and `dependency` lines as `aval.unit.yaml` at the root; only
  the `contract` line differs.
- `runner.command` and `environment.probe` run in the root.
- `--result-file`, `--report-file` and `--stats-file` are skipped only when
  they are Aval outputs under the root.
- `scope.contracts` paths stay relative to the contract file's directory.
  A referenced contract with its own `scope.root` uses its own root.

`aval explain` prints the resolved root after its header. That line is an
absolute path that differs between checkouts; it is not part of the
fingerprint. The root must lie
inside the git working tree that holds the contract (the nearest directory
at or above it with a `.git` entry). If the contract's directory is a
symlink, `..` is resolved from its real location. A root above the working
tree, or no working tree at all, exits 2 (`aval run`, `explain`, `verify`
and `push`). Globs still cannot contain `..`.

## Many tests in one file: TestSet

A `kind: TestSet` declares every test of a project in one file, without
repeating shared scope and environment:

```yaml
--8<-- "internal/fingerprint/testdata/testset/aval.yaml:set"
```

Top-level keys besides `apiVersion` and `kind`:

| Key | Holds | May set |
|---|---|---|
| `defaults` | what every test inherits | `scope.*`, `environment.*`, `cacheability` |
| `fragments` | named blocks a test pulls in with `use` | `scope.*` except `exclude`, `environment.*` |
| `tests` | a list of tests, at least one | the body of a `kind: Test` (`metadata`, `runner`, `scope`, `environment`, `outputs`, `cacheability`), plus `use` and `matrix` |

Each test expands to a standalone `kind: Test` and is fingerprinted exactly
as that file would be: the set above fingerprints `shard-1` like a
`kind: Test` named `shard-1` with the merged scope. Paths, globs and
`scope.root` resolve from the TestSet file's directory.

Merge rules, in the order `defaults`, each used fragment, then the test:

- Lists (`source`, `optionalSource`, `exclude`, `dependencies`,
  `allowMutable`, `resolve`, `contracts`, `env`) are merged as a set union. Nothing inherited can be
  removed, so a merge can only widen a test's inputs. `exclude` is the one
  way to narrow them, and it is only allowed in `defaults` or in the test
  itself, where it is visible.
- A key of `environment.toolchain` or `environment.probe`, or `scope.root`,
  set twice with different values is an error that names both sources.
- `metadata`, `runner` and `outputs` belong to a test only. The test's
  `cacheability` overrides the one in `defaults`.
- The order of `use` does not change the result. A fragment cannot `use`
  another fragment.

`matrix` maps variables to lists of strings and creates one test per
combination. It substitutes `{var}` into `metadata.name`, `runner.command`
and the values of `environment.toolchain` (inherited ones too), in one pass,
so a value that holds `{x}` is not substituted again. Each value is part of
the fingerprinted argv or toolchain lines. In a test with a matrix, every
`{name}` in those fields must be a matrix variable: write a shell variable as
`$VAR`, not `${VAR}`. In a test without a matrix, a `{name}` in a toolchain
value is an error. Probe argv, `env`, `scope` and `outputs` are not
substituted. A set expands to at most 1024 tests.

One test per platform, each with its own fingerprint, so a result recorded
on darwin is never reused on linux:

```yaml
tests:
  - metadata: {name: "unit-{os}"}
    runner: {command: [go, test, ./...]}
    matrix: {os: [linux, darwin]}
    environment:
      toolchain: {platform: "{os}"}
      probe: {platform: [go, env, GOOS]}
```

`aval run --test unit-darwin` records a result only where the probe prints
`darwin`. To reuse a result across platforms, do not declare `platform`.

Each of these exits 2 and names the problem: an unknown fragment, a fragment
no test uses, a fragment with `use`, `exclude`, `runner`, `metadata`,
`outputs`, `matrix` or `cacheability`, a conflicting key, an unknown
`{var}`, a `{var}` in a toolchain value of a test without a matrix, two
tests with the same expanded name, and a `scope.contracts` entry that points
at a TestSet (share scope with fragments instead).

### Select a test

`aval run`, `explain`, `verify` and `push` take `-t/--test <name>`, the
expanded `metadata.name` of one test:

```sh
aval run --test shard-1
aval push --test shard-1
```

- On a TestSet of more than one test, a missing `--test` exits 2 and lists
  the names. Nothing runs every test of a set at once.
- On a `kind: Test` file, or a TestSet of one test, `--test` is optional; a
  name that matches no test exits 2.
- With `--test`, the output files default to `.aval/<name>/result.json`,
  `report.json`, `record.json` and `env.txt` (`--result-file`, `--report-file`,
  `--stats-file`, `--env-file`), so tests never overwrite each other's outputs and
  `aval push --test <name>` finds the result and report of the run.
  `aval run --test <name>` removes a stale `.aval/<name>/` before it runs.
  A flag you pass wins. Without `--test` the defaults are unchanged.

`aval explain --test <name> --expand` prints the test as the standalone
`kind: Test` it expands to. `aval explain --all` checks every test of the
file without running anything (no probe, no command): it prints one
`name fingerprint` line per test, reports every failing test by name instead
of stopping at the first, and exits 2 if any is invalid, else 1 if any
fingerprint cannot be computed. `-o json` prints the same as
`{"tests": [{"name", "fingerprint"} or {"name", "error"}]}`. A file that
does not expand prints every problem on stderr and nothing on stdout.

`aval init --set` writes a TestSet of one test and one fragment, plus
`.github/aval/policy.yaml` with a placeholder identity, as a starting point
([Write a contract](guides/write-a-contract.md)).

## Nested runs

`aval run` sets `AVAL_RUN_STACK` for `runner.command` and the probes: the
absolute, symlink-resolved paths of the contracts of the enclosing runs plus
its own, joined by the OS path list separator (`:` or `;`). A run whose
contract is already in the stack exits 2 before it probes, looks up or runs
anything:

```text
aval: aval.yaml is already running in an outer aval run (/repo/aval.yaml): runner.command must not start the same contract again
```

The outer run then records a failed test (exit 3). This catches a make target
that calls `aval run` and a contract whose `runner.command` calls that target,
and a cycle through several contracts. A `runner.command` that runs `aval run`
on a different contract works. `AVAL_RUN_STACK` is not an input: it is never
hashed nor recorded, and `environment.env` cannot list it.

## Globs

`scope.source`, `scope.optionalSource`, `scope.exclude`,
`scope.dependencies` and `outputs` use the same globs:

| Glob | Matches |
|---|---|
| `src/**/*.go` | `src/a.go` and `src/x/y/a.go` |
| `*.go` | top-level files only: `*` does not cross `/` |
| `dir/**` | every file under `dir`; globs never match directories |
| `**` | every file |
| Rejected | `/a`, `./a`, `../a`, `a//b`, `a\b`, `foo**`, `**.go`, `[a-` |

- Relative to the contract root (`scope.root`, by default the contract's
  directory); a glob cannot escape it.
- Case-sensitive and byte-exact: `Makefile` is not `makefile`, NFC is not NFD.
- Order does not matter.
- Symlinks are never followed nor hashed. A symlink under which a `source`
  or `dependencies` glob could match files is an error, unless `exclude`
  covers its name and no `dependencies` glob reaches it. A symlink to a
  regular file needs only its name excluded: `tests/fx/link.yaml` skips a
  link to `real.yaml`. Any other symlink (to a directory, dangling, or a
  loop) needs its contents excluded too: `**/node_modules/**` skips
  `pkg/web/node_modules/.bin/tool`, and `["pkg/bin", "pkg/bin/**"]` skips a
  symlinked `pkg/bin`. `pkg/bin` or `pkg/bin/**` alone does not. If a
  name-excluded link later points to a directory, fingerprinting fails.

### Optional source globs

Every `source` glob must match a file, which catches typos. A test that
finds its inputs by pattern (it walks the tree for every
`*testdata*.yaml`) reads files that may not exist yet. Leaving the pattern
out of the scope means the first such file is not hashed, and a stale result
is reused. Declare it in `optionalSource`:

```yaml
scope:
  source: ["src/**"]
  optionalSource: ["**/*testdata*.yaml"]
```

- Files it matches are hashed as `source` lines, with the same `exclude`,
  symlink and path rules. Adding the first matching file is a miss; removing
  the last one returns to the earlier fingerprint.
- The only difference is that a glob may match nothing. At least one file
  from `source` and `optionalSource` together must still remain after
  `exclude`.
- `aval explain` names each optional glob that matched nothing on stderr
  (`optional: scope.optionalSource glob "<glob>" matched no files yet`), so
  check it after writing one.
- Use it only for patterns the test discovers, not to silence a `source`
  glob that matches nothing: that is usually a typo.

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
`aval-fingerprint/v2`). `aval explain` prints the exact text that was
hashed, in this order:

```text
aval-fingerprint/v2
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
| header | `aval-fingerprint/v2`, bumped when an existing contract's manifest would change | |
| `contract` | sha256 of the parsed contract as JSON | comments, formatting, key order, the order and duplicates of set-like lists (argv order counts; an empty list equals an omitted one) |
| `source`, `dependency` | sha256 of the content, `x` (any exec bit) or `-`, path. A file in both sets appears twice | owner, timestamps, other permission bits |
| `toolchain` | `key=value` as written; checked against the installed tools only by `environment.probe` | |
| `env` | sha256 of the value, or `unset` (empty is not unset); values never appear in clear, but the hash of a short or guessable value (a low-entropy secret) can be found by trying values, so declare secrets only if `aval explain` output and the [env file](artifact-format.md#env-file) may be seen. A prefix pattern gives one line per set variable it matches and none when nothing matches | |
| `resolve` | kind, reference, version or `floating`, immutable id; only with `scope.resolve`, and needs the network ([Resolve references](resolve/index.md)) | the registry address |
| `contract <path>` | one line per `scope.contracts` entry: the path as written, the referenced contract's `metadata.name` and its fingerprint, computed from its own directory and inputs (so references are transitive). `aval explain` also prints the tree, one `depends on <path> (<name>) <fingerprint>` line per reference | |

Lines are sorted within each section, so checkout path and walk order do not
matter. Paths are hashed byte for byte. Windows has no exec bit, so an
executable file fingerprints differently there than on Unix.

### Environment variable patterns

A tool configured by prefix (viper `SetEnvPrefix` with `AutomaticEnv`, Go
envconfig, pydantic-settings `env_prefix`, Spring relaxed binding) reads any
`PREFIX_<KEY>`. List the prefix instead of every name:

```yaml
environment:
  env: ["APP_*", GOFLAGS]
```

`*` may only end the entry, after at least one name character: `*`,
`*_TOKEN`, `APP*X` and `APP_?` are rejected. The pattern hashes every
variable set when the fingerprint is computed whose name starts with the
prefix, one `env NAME <sha256>` line each, so setting, unsetting or changing
one changes the fingerprint. A pattern that matches nothing adds no line; the
pattern itself is in the `contract` line. An exact name a pattern also
matches yields one line.

Names compare as the platform does: case-sensitively on Linux and macOS
(`APP_*` matches neither `APPX` nor `app_a`), case-insensitively on
Windows, where a matched name is written upper-cased in the manifest.

A pattern also hashes credentials that share the prefix, such as
`APP_REGISTRY_TOKEN`, as it would a listed name. Their sha256 digests are
in the manifest that `aval explain` and `--explain` print (CI logs), and the
published fingerprint is a hash over that manifest, so anyone who knows the
other inputs can test guesses of a short or guessable secret against it.
When the prefix covers secrets, list the exact names instead.

### What does not go in

| Not hashed | Note |
|---|---|
| files no `source`, `optionalSource` or `dependencies` glob matches | the main risk: keep the scope honest |
| `.git` (directory, file or symlink) | exact name, at any depth; covers linked worktrees and submodules. An exact glob such as `pkg/.git` matches no file and fails |
| `.aval/` directories and the command's `--result-file`/`--report-file` files | so `["**"]` stays stable; keep them under `.aval/`. A path in scope that holds a real input exits 2. In a `scope.contracts` reference they stay inputs (a miss, never a false hit) |
| commit, branch, VCS metadata | two commits with the same scope share a result |
| environment variables not in `environment.env` | |
| the real toolchain, OS and architecture | unless declared; probe output is never hashed |

### Known gaps

Declared scope means some changes are invisible by design. A test that
depends on one of these can reuse a result it should not:

- a file the test reads that `scope` does not match, including a case or
  Unicode mismatch under a wildcard glob
- an environment variable the test reads that `environment.env` does not list
- content behind a name the [scanner](pinning.md) does not recognize, or a
  network service: use `scope.resolve` or `external-dependencies`
- the installed toolchain, OS and architecture, unless `environment.probe`
  checks the toolchain entry
- git state the test reads (log, worktree or submodule layout), the same as
  commit and branch

Mitigation: review the contract, use `aval run --shadow`, pick the right
cacheability class.

### Fail-closed errors

Aval cannot prove the inputs: `aval run` reports outcome `error`, runs the
test and records nothing; `aval explain`, `aval verify` and `aval push` exit 1
(as do `run --fingerprint-only` and `run --explain`, which `aval explain --digest` and `aval explain` replace).

| Message | Fix |
|---|---|
| `source glob "<glob>" matched no files` | fix the glob; a case or Unicode mismatch also matches nothing |
| `scope.source matched no files` | `exclude` removed every `source` and `optionalSource` file, or `optionalSource` matched nothing and `exclude` removed every `source` file |
| `source "<path>" is excluded only by a trailing "**"` | exclude it by name, or narrow the glob |
| `dependency glob "<glob>" matched no files` | fix the glob |
| `only regular files are supported in scope` | remove the symlink, device or socket from scope |
| `"<path>" is a symlink a glob could match files under` | symlinks are not followed: remove it, or exclude it with no `dependencies` glob reaching it ([Globs](#globs)). The message names the fix: its name alone for a link to a regular file, its name and contents (`pkg/bin` plus `pkg/bin/**`, or `**/node_modules/**`) for anything else. The contract directory itself may be a symlink |
| `control characters in path are not supported` | rename the file (newline or carriage return) |
| `resolve <kind> <reference>: <reason>` | see [When resolution fails](resolve/index.md#when-resolution-fails) |
| `scope.contracts <path>: <reason>` | any error above in the referenced contract, or: missing or invalid contract, `leads outside the repository root` (also its `scope.root`), `no git working tree (.git) at or above <dir>`, `<path> is a symlink`, `<path> is not a regular file`, `cycle: a/aval.yaml -> b/aval.yaml -> a/aval.yaml`, `more than 8 levels or 64 contracts` |
| any I/O error while walking or reading | make the file or directory readable; the walk only enters directories a glob can match |

## Probe the toolchain

`environment.toolchain` is a claim. `environment.probe` checks it: before the
lookup, `aval run` runs each probe in the contract root, without a
shell, with a 10 s timeout and without `$SIGSTORE_ID_TOKEN`. The declared
value must appear in the output (stdout and stderr) as a version token: not
preceded by a digit or a dot, not followed by a digit. `3.13` matches
`Python 3.13.1`; it does not match `Python 3.130`, and `3.1` does not match
`Python 3.13`.

```yaml
environment:
  toolchain:
    python: "3.13"
  probe:
    python: [python3, --version]
```

On a mismatch, a probe that cannot start, a non-zero exit, a timeout or
output over 4 KiB (it is signed into the predicate),
`aval run` looks nothing up (outcome `error`; the reason names the probe, the
declared and the observed value), runs the test, records and publishes
nothing, warns `aval: warning: result not recorded: <reason>`, and exits with
the test's code (0 passed, 3 failed). With `--push` it exits 1 instead
(`result not published: <reason>`), or 3 if the test failed. On a match, the output goes into
the predicate's `execution.environment` as `probe.<key>`.

Only `aval run` executes probes. `aval verify`, `aval push`,
`aval explain` and the browser demo never do: a consumer never executes
commands from a contract. `aval explain` prints the manifest and never runs a
probe. The probe
argv is part of the contract, so adding or changing one changes the
fingerprint; its output is not hashed, since it varies by build and platform.

## Versioning

`aval.trianalab.io/v1` is frozen; policy files use the same value. Within v1
the schema only gets new optional fields. An older `aval` rejects a field it
does not know, which fails closed: a policy with the optional `registry`
field (#209) fails to load in an older `aval`, so nothing is reused. The
same holds for a kind it does not know: an older `aval` rejects a
`kind: TestSet` file with exit 2.

`aval.dev/v1alpha1` is rejected: change that line. Rules:
[artifact format](artifact-format.md#versioning-and-compatibility).

## Future work

- **Observed inputs**: trace which files the test actually reads, to flag
  undeclared inputs or suggest scope. v0 never infers scope. Until then,
  [find undeclared inputs](guides/write-a-contract.md#find-undeclared-inputs)
  by hand.
