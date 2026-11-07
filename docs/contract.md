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
| `scope.allowMutable` | list of `{ref, reason}` | no | Acknowledges a mutable reference that would otherwise block reuse (see [Pin your dependencies](pinning.md)). `ref` is the reference exactly as Aval reports it; `reason` says why it is acceptable. Both must be non-blank. An entry accepts every occurrence of that reference in the scope and in `runner.command`. Part of the contract hash. |
| `scope.resolve` | list of `kind: reference` | no | External artifacts resolved to an immutable id at fingerprint time (see [Resolve references](resolve/index.md)). Each entry has exactly one key, one of `image`, `npm`, `pypi`, `go`, `maven`, `crate`, `gem`, `git`, `url`; the reference is non-empty, without spaces or control characters, and without credentials. Rejected: a password in any URL; any user name in an `http(s)` URL or a scheme-less `//user@host` one (an `ssh://git@host` user is allowed); an `@` in the authority of a URL that does not parse; a query parameter named (in any case, `-` read as `_`, a trailing `[]` ignored) `token`, `private_token`, `access_token`, `key`, `apikey`, `api_key`, `sig`, `signature`, `X-Amz-Signature`, `X-Goog-Signature`, `auth`, `password`, `secret`, `credential`, `security_token`, `x_amz_security_token`, `client_secret`, `session` or `sessionid`. The error never echoes the reference. Part of the contract hash; order and duplicates do not matter. Never clears a scanner finding: pin the reference in place or accept it with `scope.allowMutable`. Binds the named artifact, not its dependencies. |
| `scope.contracts` | list of paths | no | Other contracts this test depends on (see [Depend on another part of the repo](guides/depend-on-a-contract.md)). Each path is relative to this contract's directory: clean (`..` only at the start), slash-separated, without spaces, ending in `.yaml` or `.yml`, listed once. At fingerprint time the file must exist, be a valid contract and lie inside the git working tree of this contract (the nearest directory at or above it with a `.git` entry). No component of the path may be a symlink. No cycles, at most 8 levels and 64 distinct contracts. A referenced contract's mutable references block reuse here too, unless its own `scope.allowMutable` accepts them. Part of the contract hash; order does not matter. |
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
  every file. `foo**` or `**.go` are rejected. Consecutive `**` match the
  same names as one (`**/**/x` is `**/x`), and matching takes time
  proportional to glob segments times path segments, however many `**`.
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
| `non-deterministic` | Results vary between runs with identical inputs. | never |

"Reusable by default" only means the class does not block reuse. A result
is still reused only if a valid, signed attestation exists and the trust
policy accepts its signer (see [trust-model.md](trust-model.md#policy)).
Without a policy nothing is accepted.
`aval run` prints `cacheability external-dependencies: not reusable by
default (the policy may allow it)` and `cacheability non-deterministic: never
reusable`. A `non-deterministic` test is never looked up: it always runs.

A `hermetic` or `controlled-environment` test whose scope or
`runner.command` holds a mutable reference (a container tag, a manifest
without a lockfile, a script fetched at run time) that `scope.allowMutable`
does not acknowledge is never looked up either: the outcome is `miss`, the
reason lists each reference, the test runs and its result is not published.
For `external-dependencies` the references are printed as information. See
[Pin your dependencies](pinning.md).

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
resolve <kind> <reference> <version> <immutable id>
contract <path> <name> <fingerprint>
```

- **Header**: `aval-fingerprint/v1`. Bumped whenever the manifest of an existing contract would change, so
  fingerprints from different formats never collide.
- **contract**: SHA-256 of the contract serialized as JSON after parsing.
  Comments, formatting and YAML key order do not matter. Set-like lists
  (`source`, `exclude`, `dependencies`, `env`, `allowMutable`, `resolve`, `contracts`) are sorted
  and deduplicated first, so their order does not matter. `runner.command` is not sorted:
  argv order is semantic. Map keys (`toolchain`) are sorted by the JSON
  encoder. Every field is included, so changing `metadata.name`,
  `runner.command` or `cacheability` changes the fingerprint. An explicitly
  empty list or map (`exclude: []`) hashes the same as an omitted field.
  `allowMutable`, `resolve` and `contracts` are left out of the JSON when empty, so
  contracts without them hash as they did before the fields existed.
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
- **resolve**: one line per `scope.resolve` entry: its kind, the reference
  as written, what it resolved to (a version or ref name; `floating` for a
  range, a tag or latest; `-` when there is none) and the immutable id
  (digest, checksum, commit, or for `floating` the hash of the whole release
  list) read from the registry when the fingerprint is computed. Only contracts with `scope.resolve` have these
  lines, and only they need the network to fingerprint (see
  [Resolve references](resolve/index.md)). The registry address is not
  hashed.
- **contract** with a path: one line per `scope.contracts` entry: the path
  as written, the referenced contract's `metadata.name` and its
  fingerprint, computed from its own directory, scope, environment,
  `scope.resolve` and `scope.contracts` (so references are transitive).
  Only contracts with `scope.contracts` have these lines. `--explain` also
  prints the tree, one `depends on <path> (<name>) <fingerprint>` line per
  reference, indented by level.

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
- `.git` directories at any depth (skipped entirely). The name match is
  exact and case-sensitive: a `.GIT` directory is not skipped. A regular
  file named `.git` (a worktree or submodule pointer) is an ordinary file:
  it is in scope if a glob matches it.
- Aval's own outputs, so a broad scope such as `["**"]` keeps its
  fingerprint across `aval run`s and `aval push` accepts the recorded
  result:
  - `.aval` directories at any depth (skipped entirely, the same exact,
    case-sensitive name match as `.git`). `.aval/` is reserved for Aval: a
    file the test reads must not live there.
  - The `--result` and `--report` files the command is given, when they
    fall inside the contract root. Each command knows only its own flags:
    `aval push` skips its `--result`, `aval verify` neither. A custom path
    outside `.aval/` is therefore hashed by a command that is not given it
    (a miss, never a false hit); keep results and reports under `.aval/`
    or outside the root. A `--result` or `--report` path that the scope
    includes and that holds anything other than an Aval result or report
    (a real input) exits 2 before the test runs: skipping it would drop
    that input from the fingerprint. `aval run` also refuses, with exit 2,
    to overwrite any existing file at those paths that is not an Aval result
    or report, wherever it lies. Only this contract's scope skips them: in
    a `scope.contracts` reference they stay inputs (a miss, never a false
    hit).
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
  service reached over the network. Only the declared text is hashed,
  unless the reference is in `scope.resolve`, which hashes its immutable
  id ([Resolve references](resolve/index.md)). The
  references the mutable-reference scanner recognizes block reuse for
  `hermetic` and `controlled-environment` (the `refused` corpus rows):
  images without a digest, manifests without a lockfile, unpinned installs,
  scripts piped from the network, branch-tracking git references. See
  [Pin your dependencies](pinning.md) for the list and the fixes per
  ecosystem. A reference the scanner does not recognize, and a service
  reached over the network, remain invisible: use `external-dependencies`,
  which is not reusable by default.
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
(out-of-scope or excluded files, `.git`, Aval's own outputs, checkout path, ordering,
duplicates, timestamps, non-exec permission bits, YAML formatting) does not;
an unprovable one (see "Fail-closed errors") fails closed; a repushed
image tag (in a compose file or in `runner.command`) keeps the fingerprint
but is reported as an unacknowledged mutable reference, so it is refused.
The scanner has its own false-hit table per ecosystem, mutable and pinned
variants, in `internal/pinning`, and a fuzz target over every detector. The test fails if
a manifest section is changed by no differ row, so a new section must come
with corpus cases; a new attribute in an existing section or a new contract
field needs its row added by review.

`TestResolveFalseHits` runs the same outcomes through the real resolvers
and in-process fakes: a republished download or a repushed image tag in
`scope.resolve` changes the fingerprint, reordering or repeating entries
does not, and an unresolvable reference fails closed; `internal/resolve`
has the per-ecosystem tables. `TestResolveManifestGolden` pins the
`resolve` lines in `testdata/golden/resolve-aval-fingerprint-vN.manifest`.

`TestContractsFalseHits` and `TestContractsFailClosed` cover
`scope.contracts`: a change inside a referenced contract's scope (also two
levels down) changes the dependent's fingerprint, a change outside it does
not, and a missing, invalid, escaping, symlinked, cyclic or too deep
reference fails closed. `TestContractsFindings` checks that a referenced
contract's mutable references block the dependent. `TestContractsManifestGolden`
pins the `contract <path>` lines in
`testdata/golden/contracts-aval-fingerprint-vN.manifest`.

`TestManifestGolden` pins, per manifest version, the manifests of three
contracts over a fixed tree (`internal/fingerprint/testdata/golden/tree`),
concatenated in `testdata/golden/aval-fingerprint-vN.manifest`. One
contract sets every field: `source`, `exclude`, `dependencies` and `env`
each unsorted and with a repeat, a toolchain map with six keys, argv with
`&`, `<`, `>` and non-ASCII characters, and env vars that are set, set to
the empty string and unset. One omits every optional field; one sets every
optional field to an empty list or map and must encode like the one that
omits them (`TestGoldenOmittedEqualsEmpty`). The tree has a nested path, a
path with a space, a file that sorts differently by full path than in walk
order (`pkg.go` before `pkg/...`), an empty file under a dot-directory,
content with CRLF line endings, an excluded file and a file outside the
scope. The test adds a top-level and a nested `.git` directory (skipped)
and a regular file named `.git` (in scope), and fixes the env values and
the file modes: one exec bit each for owner (0744), group (0654) and other
(0645), all three (0755), and a file with setuid, setgid and sticky but no
exec bit. Any change to how these manifests are built (their lines, the
hashes in them or the canonical contract JSON) fails it unless the header
version is bumped too, so an old fingerprint can never mean different
inputs.

What this covers was measured by mutation: each conditional, constant,
format string and normalization in `Compute`, `canonicalContract`,
`listFiles`, `hashMatching`, `hashFile`, `sum` and the glob matcher was
mutated and `make ci-false-hit` run; every mutant that changes the manifest
of some input fails it, through the golden, the corpus, the glob tables or
the fail-closed tests. The two mutants that pass are equivalent on the
platforms CI runs: dropping `filepath.ToSlash` (the separator is already
`/` on Unix) and masking with `Mode().Perm()&0o111` instead of
`Mode()&0o111`. A path the tree does not exercise (a new manifest feature)
is caught only by the corpus, so it must come with a case in the golden
tree.

`TestGoldensAppendOnly` stops the golden being rewritten to match: a
committed `*.manifest` golden may be added (a new version) but never
modified, renamed or deleted. It diffs the goldens against the merge base of
`HEAD` and `AVAL_GOLDEN_BASE`; in CI that is the PR base branch or the commit
before the push, and the test fails if it does not resolve (after a
force-push to `main` the old commit may be unreachable, and the job fails
until the next normal push). Locally `make ci-false-hit` sets it to
`origin/main` when it is unset, and the test fails when that ref is missing,
so set `AVAL_GOLDEN_BASE` to the upstream main if your remote is named
differently. A plain `go test` without `AVAL_GOLDEN_BASE` (the `ci-engine`
job and the dogfood suite, which check out shallow) skips this one test;
the `ci-false-hit` leg never does. `TestGoldenEditsCatchesEdits` is
its negative test; by hand, append a line to the golden and run
`AVAL_GOLDEN_BASE=HEAD go test ./internal/fingerprint -run TestGoldensAppendOnly`.
The `ci-false-hit` CI job (`make ci-false-hit`, part of `make ci`) runs these
tests and the glob tables on every PR and push, with no path filter.
`TestFalseHitGateIsBinding` (`tests/architecture`) fails if that job or leg
is removed, made conditional, allowed to fail (`continue-on-error`), checked
out shallow, or given any other `AVAL_GOLDEN_BASE`, or if the recipe of
`make ci-false-hit` changes in any way (a `-skip`, a dropped package, a
dropped `-count=1`, a dropped `AVAL_GOLDEN_BASE` default). Edits to the tests themselves are left to review.

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
- A symlink under which a `source` or `dependencies` glob could match files
  (it may point to a directory):
  `"<path>" is a symlink a glob could match files under`. Symlinks are not
  followed, and `exclude` does not lift this check. A symlink no glob can
  reach is ignored. The contract directory itself may be a symlink: it
  fingerprints like its target.
- A path in scope contains a newline or carriage return: `control characters
  in path are not supported`. The manifest is line-oriented, so such a name
  could forge manifest lines and collide with a different tree.
- A `scope.resolve` reference that cannot be resolved: `resolve <kind>
  <reference>: <reason>` (network, credentials, an exact version that is not published, a
  download over 256 MiB, the 2-minute deadline).
- A `scope.contracts` reference that cannot be proven: `scope.contracts
  <path>: <reason>`. The reason is any error above in the referenced
  contract, or: the file is missing or not a valid contract; `leads outside
  the repository root`; `no git working tree (.git) at or above <dir>`; `<path> is a
  symlink`; `<path> is not a regular file`; `cycle: a/aval.yaml -> b/aval.yaml -> a/aval.yaml`; `more than
  8 levels or 64 contracts`.
- Any I/O error while walking or reading the root. The walk only enters
  directories that a `source` or `dependencies` glob can match a file in, so
  an unreadable directory outside the scope (a root-owned volume, a cache)
  is never opened and causes no error. `exclude` does not prune the walk.

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
