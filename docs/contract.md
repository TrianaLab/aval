# The test contract: `aval.yaml`

Status: `aval.trianalab.io/v1`, frozen. Within v1 the schema changes additively
only (see [Versioning](#versioning)).
Source of truth: `internal/contract` (schema, validation) and
`internal/fingerprint` (what gets hashed). If this document and the code
disagree, the code wins and this document has a bug.

The contract declares what a test is, how to run it, and which inputs it
depends on. Aval reuses a previous result only when it can prove that the
declared inputs are identical. Nothing is inferred: if an input is not
declared, Aval cannot see it.

## Example

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

environment:
  toolchain:                    # optional, declared, not detected (v0)
    go: "1.25"
  env:                          # optional, names only; values are hashed
    - CGO_ENABLED
    - GOFLAGS

cacheability: hermetic          # required, see "Cacheability classes"
```

Check it locally:

```sh
aval run -f path/to/aval.yaml --fingerprint-only   # prints sha256:<hex>
aval run -f path/to/aval.yaml --explain --fingerprint-only   # also prints the manifest
```

## Fields

All fields below are the complete v1 schema. Unknown fields at any
level are rejected (strict YAML decoding), so a typo such as `sorce:` fails
instead of silently shrinking the scope. A file with more than one YAML
document is rejected. Semantic validation reports all errors at once; YAML
decoding errors (including unknown fields) are reported one at a time. An invalid contract exits with code 2 (usage).

| Field | Type | Required | Rules |
|---|---|---|---|
| `apiVersion` | string | yes | Must be `aval.trianalab.io/v1`. |
| `kind` | string | yes | Must be `Test`. |
| `metadata.name` | string | yes | 1-64 chars of `[a-z0-9._-]`, starting and ending with `[a-z0-9]`. |
| `runner.command` | list of strings | yes | Non-empty argv. The first element must not be blank. No shell interpolation. v0 only supports this generic runner. |
| `scope.source` | list of globs | yes | At least one glob. Matched files are hashed. At fingerprint time, each glob must match at least one file (before `exclude`), and the set left after `exclude` must not be empty. |
| `scope.exclude` | list of globs | no | Removes files from the `source` set. Does not apply to `dependencies`. A glob may match nothing. |
| `scope.dependencies` | list of globs | no | Lockfiles and manifests. Each glob must match at least one file at fingerprint time. |
| `environment.toolchain` | map string to string | no | Keys match `^[a-z0-9][a-z0-9._-]*$`. Values are not validated. |
| `environment.env` | list of strings | no | Environment variable names matching `^[A-Za-z_][A-Za-z0-9_]*$`. |
| `cacheability` | string | yes | One of `hermetic`, `controlled-environment`, `external-dependencies`, `non-deterministic`. |

## Globs

All globs in `scope.source`, `scope.exclude` and `scope.dependencies` follow
the same rules.

- Slash-separated, relative to the directory that contains the contract file.
  That directory is the fingerprint root. Backslashes are rejected.
- No leading `/`. No empty, `.` or `..` segments (`a//b`, `./a`, `../a` are
  rejected), so a glob cannot escape the root. Likewise, a path with a
  leading `/` or such segments never matches any glob, not even `**`.
- Each segment is matched with Go's `path.Match` (`*`, `?`, `[...]`). `*`
  does not cross `/`. A malformed pattern (e.g. `[a-`) is rejected.
- `**` is only allowed as a whole segment and matches zero or more segments.
  `src/**/*.go` matches `src/a.go` and `src/x/y/a.go`. `**` alone matches
  every file. `foo**` or `**.go` are rejected.
- In `exclude`, a trailing `**` means the files under a directory: a file
  named `build` that only `build/**` would exclude fails fingerprinting
  (see "Fail-closed errors"). Exclude such a file by name (`build`).
- Globs match files only, never directories. To include a directory's
  contents, write `dir/**`.
- The order of globs does not matter.

## Test identity vs execution fingerprint

- **Test identity** is `metadata.name`: what is being checked, stable across
  executions (e.g. `orders-integration`).
- **Execution fingerprint** is a SHA-256 over the concrete inputs and
  conditions of one execution: the contract itself, the content of every
  file in scope, the declared toolchain and the declared environment values.
  Two executions with the same fingerprint are, as far as the contract can
  tell, the same execution.

The name is also part of the fingerprint (through the contract hash), so
renaming a test produces a new fingerprint.

## Cacheability classes

| Class | Meaning | Reusable by default |
|---|---|---|
| `hermetic` | Result depends only on declared files and toolchain. | yes |
| `controlled-environment` | Depends on services/images whose versions are pinned inside the scope (e.g. a compose file with image digests). | yes |
| `external-dependencies` | Talks to things outside the scope (SaaS APIs, shared databases). | no, only with explicit policy opt-in |
| `non-deterministic` | Results vary between runs with identical inputs. | no |

"Reusable by default" only means the class does not block reuse. A result
is still reused only if a valid, signed attestation exists and the trust
policy accepts its signer (see [trust-model.md](trust-model.md#policy)).
Without a policy nothing is accepted.
`aval run` prints `cacheability <class>: not reusable by default` for the
last two classes. A `non-deterministic` test is never looked up: it always
runs.

## What goes into the fingerprint

The fingerprint is `sha256:<hex>` of a canonical, line-oriented manifest
(format `aval-fingerprint/v1`). `aval run --explain` prints the exact text
that was hashed. Layout, in this order:

```text
aval-fingerprint/v1
contract <sha256 of canonical contract JSON>
source <sha256> <mode> <path>
dependency <sha256> <mode> <path>
toolchain <key>=<value>
env <NAME> <sha256 of value | unset>
```

- **Header**: `aval-fingerprint/v1`. Bumped whenever the layout changes, so
  fingerprints from different formats never collide.
- **contract**: SHA-256 of the contract serialized as JSON after parsing.
  Comments, formatting and YAML key order do not matter. Set-like lists
  (`source`, `exclude`, `dependencies`, `env`) are sorted and deduplicated
  first, so their order does not matter. `runner.command` is not sorted:
  argv order is semantic. Map keys (`toolchain`) are sorted by the JSON
  encoder. Every field is included, so changing `metadata.name`,
  `runner.command` or `cacheability` changes the fingerprint. An explicitly
  empty list or map (`exclude: []`) hashes the same as an omitted field.
- **source**: one line per file that matches any `source` glob and no
  `exclude` glob.
- **dependency**: one line per file that matches any `dependencies` glob.
  A file matched by both `source` and `dependencies` appears in both sections.
- File lines carry the SHA-256 of the file content, a portable mode (`x` if
  any execute bit is set, `-` otherwise) and the slash-separated path
  relative to the root. Owner, timestamps and other permission bits are not
  hashed.
- **toolchain**: one line per declared entry, `key=value`, as written. v0
  trusts the declaration; it does not check the installed toolchain.
- **env**: one line per declared variable with the SHA-256 of its value, or
  `unset` if the variable is not set. Set-but-empty and unset are different.
  Values are never written in clear.

Lines are sorted within each section. The result does not depend on checkout
path, directory iteration order or glob order. The `x` mode is taken from the
filesystem, so Windows (which has no execute bit) and Unix checkouts of the
same executable file fingerprint differently.

Paths are hashed as the bytes the filesystem returns, never normalized. The
NFC and NFD spellings of the same visible name (`café.go`) are different
inputs, and so are `A.go` and `a.go`. Globs match those bytes
case-sensitively and without normalization. A literal glob that differs from
the on-disk name only by case or Unicode form (`Makefile` vs `makefile`)
matches nothing, so fingerprinting fails (see "Fail-closed errors"). A
wildcard glob that matches other files (`**/Makefile`) still leaves such a
file out of scope, like any undeclared file.

### What does not go in

- Files not matched by `source` or `dependencies`. Undeclared inputs are
  invisible: if the test reads them, Aval cannot detect a change. This is
  the main risk of declared scope; keep the scope honest.
- `.git` directories (skipped entirely).
- The git commit, branch or any VCS metadata. Two commits with identical
  scope content produce the same fingerprint and can reuse the same result.
  That is intended: a commit that only touches unrelated files should not
  force a re-run.
- Environment variables not listed in `environment.env`.
- The real toolchain version, OS and architecture, unless declared.

### Known gaps

Declared scope means some relevant changes are invisible by design
(invariant 2 in `AGENTS.md`). A test that depends on one of these can reuse a
result it should not. The first two are pinned by `gap` rows in the
false-hit corpus (`internal/fingerprint/corpus_test.go`), which assert the
fingerprint does *not* change; the others follow from what the manifest
hashes (see above):

- A file the test reads but `scope` does not match (corpus row), including
  a name that differs from a wildcard glob by case or Unicode form.
- An environment variable the test reads but `environment.env` does not
  list (corpus row).
- Content behind a declared name: a container tag such as `postgres:16`
  that is repushed, a dependency fetched from the network at run time, a
  service reached over the network. Only the declared text is hashed. Pin
  digests inside the scope (`controlled-environment`) or use
  `external-dependencies`, which is not reusable by default.
- The installed toolchain, OS and architecture: `environment.toolchain` is
  trusted as written (see "Future work").

Mitigations: contract review, the cacheability classes, `aval run --shadow`
(runs the test anyway and fails on a false hit) and, in the future, observed
inputs.

### False-hit corpus

`TestFalseHitCorpus` applies every known kind of change to one base project
and asserts its outcome: a relevant change (content, rename, add, delete,
exec bit, lockfile, toolchain, env value or presence, command, any contract
field, re-included excludes) changes the fingerprint; an irrelevant one
(out-of-scope or excluded files, `.git`, checkout path, ordering,
duplicates, timestamps, non-exec permission bits, YAML formatting) does not;
an unprovable one (see "Fail-closed errors") fails closed. The test fails if
a manifest section is changed by no differ row, so a new section must come
with corpus cases; a new attribute in an existing section or a new contract
field needs its row added by review.

`TestManifestGolden` pins, per manifest version, the manifests of three
contracts over a fixed tree (`internal/fingerprint/testdata/golden/tree`),
concatenated in `testdata/golden/aval-fingerprint-vN.manifest`. Between them
they reach every encoding branch of the manifest today: one contract sets
every field (unsorted and repeated lists, a toolchain map, argv with `&`,
`<`, `>` and non-ASCII characters, an exclude, a dependency, and env vars
that are set, set to the empty string and unset); one omits every optional
field; one sets every optional field to an empty list or map and must encode
like the one that omits them (`TestGoldenOmittedEqualsEmpty`). The tree has
a nested path, a path with a space, an empty file under a dot-directory,
a `.git` directory (made by the test), an excluded file, a file outside the
scope and a file with only the group exec bit; the test fixes the file
modes and the env values. Any change to how these manifests are built (their
lines, the hashes in them or the canonical contract JSON) fails it unless
the header version is bumped too, so an old fingerprint can never mean
different inputs. A change to a path the tree does not exercise is caught
only by the corpus, so a new manifest feature must come with a case in the
golden tree.

`TestGoldensAppendOnly` stops the golden being rewritten to match: a
committed `*.manifest` golden may be added (a new version) but never
modified, renamed or deleted. It diffs the goldens against the merge base of
`HEAD` and `AVAL_GOLDEN_BASE`; in CI that is the PR base branch or the commit
before the push, and the test fails if it does not resolve (after a
force-push to `main` the old commit may be unreachable, and the job fails
until the next normal push). Locally the default is `origin/main`, and the
test is skipped when that ref is missing. `TestGoldenEditsCatchesEdits` is
its negative test; by hand, append a line to the golden and run
`AVAL_GOLDEN_BASE=HEAD go test ./internal/fingerprint -run TestGoldensAppendOnly`.
The `ci-false-hit` CI job (`make ci-false-hit`, part of `make ci`) runs these
tests and the glob tables on every PR and push, with no path filter.
`TestFalseHitGateIsBinding` (`tests/architecture`) fails if that job or leg
is removed, made conditional, checked out shallow, or given any other
`AVAL_GOLDEN_BASE`. Edits to the tests themselves are left to review.

### Fail-closed errors

If equivalence of inputs cannot be proven, fingerprinting fails and the
test must run. `aval run` reports the reason (outcome `error`) and runs the
test without recording it; `aval run --fingerprint-only`, `--explain`,
`aval verify` and `aval push` exit with code 1. The cases:

- `source glob "<glob>" matched no files`: every `source` glob must match
  at least one file before `exclude` is applied. A typo or a name that
  differs by case or Unicode form would otherwise silently drop a declared
  input.
- `scope.source matched no files` (after applying `exclude`).
- `source "<path>" is excluded only by a trailing "**"`: an `exclude` glob
  such as `build/**` matches a file named `build` only because `**` matches
  zero segments. Exclude the file by name or narrow the glob.
- `dependency glob "<glob>" matched no files`: every `dependencies` glob
  must match at least one file.
- A file in scope is not a regular file (symlink, device, socket, ...):
  `only regular files are supported in scope`. A symlink's target may live
  outside the scope, so it cannot be proven unchanged.
- A path in scope contains a newline or carriage return: `control characters
  in path are not supported`. The manifest is line-oriented, so such a name
  could forge manifest lines and collide with a different tree.
- Any I/O error while walking or reading the root.

## Debugging

```sh
aval run -f aval.yaml --fingerprint-only            # sha256:<hex>
aval run -f aval.yaml --explain --fingerprint-only  # manifest, then digest
```

To find out why two environments disagree, diff the `--explain` output from
both. The differing line names the file, toolchain entry or variable.

`-f` defaults to `aval.yaml` in the current directory. The root is always the
directory of the contract file, not the current directory.

## Versioning

`apiVersion` `aval.trianalab.io/v1` is frozen. Policy files (`kind: Policy`)
use the same value. Within v1 the schema only gets new optional fields:
existing contracts keep parsing and keep their meaning. Because unknown
fields are rejected, an older `aval` rejects a contract that uses a field
it does not know, which fails closed. Renaming or removing a field, changing
its type or meaning, or tightening its validation needs a new `apiVersion`.

The pre-freeze `aval.dev/v1alpha1` is not accepted: change the line to
`aval.trianalab.io/v1`. The contract hash covers `apiVersion`, so results
published under the old value become misses and the tests run once. The
general rules are in
[artifact-format.md](artifact-format.md#versioning-and-compatibility).

## Future work

- **Detected toolchain**: verify or record the installed toolchain instead
  of trusting `environment.toolchain`. Not scheduled in milestones 1-5.
- **Observed inputs**: trace which files the test actually reads, to flag
  undeclared inputs or suggest scope. v0 never infers scope.
