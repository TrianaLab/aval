# Generate scope from the dependency graph

Goal: one test per unit (package, module, project) whose scope is the unit's
files plus the files of every unit it depends on. A change to a unit re-runs
its test and the tests of the units that depend on it; every other test is
reused.

`aval gen <language>` writes that scope into a `kind: TestSet` from the
repository's real dependency graph. You run it while authoring and commit the
result: `aval run`, `verify` and `push` never call it, and the scope stays
declared in `aval.yaml`.

!!! note "Languages"
    Go ([below](#go)), JavaScript and TypeScript
    ([below](#javascript-and-typescript)), Python ([below](#python)), Rust
    ([below](#rust)), .NET ([below](#net)), and Java and Kotlin
    ([below](#java-and-kotlin)) ship, as part of
    [Epic 16](https://github.com/TrianaLab/aval/issues/291). `aval gen --help`
    lists the languages your build has.

## What it writes

Given units `a` and `b` where `a` imports `b`, `aval gen go` adds (leaving
out the shared [`go.module`](#the-gomodule-fragment) fragment that each test
also uses):

```yaml
fragments:
  go.a:
    scope:
      source:
        - a/*.go
  go.b:
    scope:
      source:
        - b/*.go
tests:
  - metadata: {name: go.a}
    runner: {command: [go, test, ./a]}
    use:
      - go.a
      - go.b
  - metadata: {name: go.b}
    runner: {command: [go, test, ./b]}
    use:
      - go.b
```

- One fragment per unit with its own files, one test per unit that `use`s
  the fragments of the unit and of everything it reaches. Aval computes the
  closure; a dependency cycle puts every unit on it in each other's tests.
- Names are `<language>.<unit>`: the unit lower-cased, `/` turned into `.`,
  other characters outside `[a-z0-9._-]` into `-`. A name over 64 characters
  is cut and ends in a hash of the unit. A name depends on its unit alone, so
  adding a unit never renames another. Two units that get the same name are
  an error.
- Entries are sorted by name, so the output is the same on every run and
  platform.
- Real file and directory names become globs that match exactly them: `*`,
  `?` and `[` turn into `[*]`, `[?]` and `[[]`, so a package in `libs/a[1]`
  gets `libs/a[[]1]/**`. A name no contract glob can express (one with a
  backslash) fails (exit 1) and nothing is written; rename it.

## Ownership

`aval gen go` owns the fragments and tests named `go.*` and nothing else:

- It replaces those entries in place (the first one's position, or the end
  of `fragments` and `tests`) and keeps every other line byte for byte,
  comments and blank lines included. Comment lines right above a later
  generated entry go with it, and a missing final newline is added.
  Generated lines use the file's line endings (`\n`, `\r\n` or `\r`).
- The prefix is reserved: a hand-written `go.*` entry is replaced.
- Two generators share a file: `aval gen js` owns `js.*` and leaves `go.*`
  alone.
- `fragments` and `tests` must be block style (not `{}` or `[]`).

Other shared inputs stay hand-written. Put lockfiles, root build files,
the toolchain, probes, `env` and `cacheability` in `defaults` or in your own
fragments; every generated test inherits `defaults`. `aval gen go` writes
its shared inputs itself, in [`go.module`](#the-gomodule-fragment).

```yaml
apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults:
  scope:
    dependencies: [Makefile]
  cacheability: hermetic
fragments:
  go.module:                # generated, see below; shortened here
    scope:
      dependencies: [go.mod, go.sum]
  go.web:
    scope:
      source:
        - web/*.go
tests:
  - metadata: {name: e2e}   # hand-written, kept as is
    use: [go.web]           # uses a generated fragment
    runner: {command: [make, e2e]}
  - metadata: {name: go.web}
    runner: {command: [go, test, ./web]}
    use:
      - go.module
      - go.web
```

A hand-written test can `use` generated fragments. When the unit behind one
disappears, `aval gen` fails (exit 2) and writes nothing.

`--no-test <pattern>` (repeatable, a contract glob over unit IDs such as
`cmd/gen*` or `tests/**`) writes no test for the matching units, for code
generators or harness packages you keep out of the test gate: their fragment
stays while another test uses it, so their files stay in that test's scope. A
pattern that matches no unit fails (exit 2), and `--check` needs the same
flags.

`--list-tests` writes nothing and prints the IDs of the units that get a
test under the same `--no-test` patterns, one per line. A pattern that
matches no unit fails as in `aval gen`. Keep the patterns once, in a Makefile for example, and derive both
`aval gen` and your own gate from them, so the two lists cannot drift:

```make
NO_TEST := --no-test 'cmd/gen*' --no-test 'tests/**'

gen:
	aval gen go $(NO_TEST)

gen-check:
	aval gen go $(NO_TEST) --check

cover:  # the coverage gate covers what gets a test
	go test -coverprofile=cover.out $$(aval gen go $(NO_TEST) --list-tests | sed 's|^|./|')
```

IDs are per language (a Go package directory, a crate or package name).
The Go root package's ID is the last element of its import path, not `.`:
in a module with a root package, map that ID to `.` before `go test`.

## Go

`aval gen go` reads the graph with `go list -deps -test -json ./...` in the
contract root, so it needs `go` on `PATH`. A module or a `go.work` workspace
works. Each package is one unit:

- Its id is the package directory (`internal/orders`); the root package
  takes the last element of its import path (`shop`).
- Its files are `<dir>/*.go`, its non-Go sources (C, assembly, `.syso`)
  and `<dir>/testdata/**` when `testdata` holds a file. A package that
  uses `go:embed` (test embeds included) gets its whole directory,
  `<dir>/**`, so a file added later under an embedded directory is in
  scope without regenerating.
- Its dependencies are the packages of the main modules it imports, from
  any `.go` file, test files and files under any build tag included. The
  standard library and external modules are not units: `go.module` covers
  them through `go.mod` and `go.sum`.
- Its files also include what its code and tests read at run time by a
  path `aval gen go` can resolve, such as `../../fixtures/order.json`
  ([below](#files-a-package-reads)).
- Its test is `go test ./<dir>` and `use`s `go.module`.

### Files a package reads

Tests often read files outside their package. `aval gen go` finds the
reads in every `.go` file of a package, tests included, and adds their
files to the package's fragment:

```go
// internal/orders/orders_test.go
const fixtures = "../../fixtures"

func TestFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixtures, "order.json"))
	// ...
}
```

```yaml
fragments:
  go.internal.orders:
    scope:
      source:
        - fixtures/order.json
        - internal/orders/*.go
```

An edit to `fixtures/order.json` re-runs `go.internal.orders` and the
tests of the packages that import it, and no other test.

- Reads: `os.ReadFile`, `Open`, `OpenFile`, `ReadDir`, `Stat`, `Lstat`,
  `Readlink`, `DirFS`, `OpenRoot` and `OpenInRoot`; `ioutil.ReadFile` and
  `ReadDir`; `filepath.Walk`, `WalkDir` and `Glob`; the `html/template`
  and `text/template` functions `ParseFiles` (every argument) and
  `ParseGlob`; `http.Dir`; and `os.Chdir`, `syscall.Chdir`,
  `unix.Chdir`, `t.Chdir` and an open file's `Chdir`, since later
  relative reads land under that directory (going back to the directory
  `os.Getwd` returned is not a read). Dot imports count.
- Paths passed to other functions: in a test file, every argument of a
  call (to any function, of another package too) and every element of a
  composite literal (table rows) that names a file or directory of the
  contract root outside the package directory adds it, so
  `contract.Load("../../examples/shop.yaml")` puts
  `examples/shop.yaml` in scope. For a value it cannot resolve, it adds
  the directory a literal with a `..` segment names
  (`fmt.Sprintf("../fixtures/%s.json", name)` adds `fixtures/**`). These
  never fail.
- After a `Chdir`, a relative read of the package resolves against the
  package directory and against every directory its `Chdir` targets
  reach, in any order; a candidate outside the contract root fails.
- Paths it resolves: string literals and constants, `+`, `filepath` and
  `path` `Join`, `Clean`, `Dir`, `Abs`, `FromSlash` and `ToSlash`, every
  value assigned to a variable of the package, ranges over slice and map
  literals, fields of struct literals (table tests) and of what a function
  of the package returns, results and parameters of the package's own
  functions and methods (from every call in the package), function
  literals called directly, through a variable or a table field,
  constants, variables and function results of the other packages of the
  main modules, a directory entry's `Name()` (any file of the directory
  `ReadDir` read), and the source file `runtime.Caller(0)` returns.
- A path is relative to the package directory, where `go test` runs. A
  file becomes its own glob, a directory `<dir>/**` (for `Glob`, the
  directory before the first wildcard). A path that does not exist adds
  nothing; once it exists, `--check` reports the drift.
- A path under a temporary directory is not a file of the repository:
  one built on `t.TempDir()`, `b.TempDir()`, `os.TempDir()`,
  `os.MkdirTemp` or the `Name()` of an `os.CreateTemp` file, through
  variables, struct fields, helpers of the package or of another one,
  even with a `..` after it. Neither is the name of a test (`t.Name()`),
  a match of `filepath.Glob` (its directory is the read) or a parameter
  of a function whose callers it cannot see, such as a closure handed to
  other code: what those callers pass is checked where they are.

A read in a non-test file reads what its callers pass, so only paths
built from literals and constants there count: they are added like a
test's, and one outside the contract root fails. A read of a parameter,
a field or anything else it cannot resolve adds nothing and does not
fail; the test that calls it is checked where it is.

A read in a test file fails closed, naming the file and line (exit 1,
nothing written):

- when its path cannot be resolved and may leave the package directory:
  one built from `os.Getwd`, `os.Executable`, an environment variable
  (`os.Getenv`, `LookupEnv`, `ExpandEnv`) or `runtime.Caller` with
  another depth, or from a literal with a `..` segment that names a file
  or directory of the contract root outside the package
  (`filepath.Join(root, name)` with `root` from `"../.."`), also through
  the directory a template names (`fmt.Sprintf("../fixtures/%s.json",
  name)`). Paths it cannot follow count as built from anywhere: a read
  function itself used as a value (`var readFile = os.ReadFile`, other
  than assigned to `_`); a variable written through a pointer; a call to
  a method of another package or to a function value it cannot trace; a
  function of another package that is not of the main modules (other
  than `strings`, `fmt`, `strconv`, `path` and `filepath`, which keep what
  their arguments may climb to); a value too large to follow; and a
  `Chdir` to a directory it cannot resolve, such as an open file's;
- when its path is outside the contract root, absolute ones included
  (`/proc/self/cgroup`).

`--go-allow-dynamic <pattern>` (repeatable, a contract glob over package
IDs such as `tests/**`) turns those errors off for the packages it
matches; files it resolves are still added. Declare the rest by hand:
in `defaults`, which every test inherits, or with `--no-test` for the
package and a hand-written test that `use`s its `go.*` fragments and a
fragment of your own. A pattern that matches no package fails, and
`--check` needs the same flags.

It does not see, so declare by hand: a path a test computes from the
working directory, the executable, the environment or `runtime.Caller`
and passes to a function that reads it (the read fails only where it is
in a test file); a relative literal in a non-test file read by another
package's test, which runs in that package's directory (it is resolved
against its own); a slice or map literal that a function it is passed
to changes; a path that climbs without a `..` in any literal
(`"." + "./x"`, `strings.TrimPrefix("x../../y", "x")`); a `..` added
after a temporary directory that climbs back into the repository; a
`Chdir` the tests run more than once in a row (`os.Chdir("..")` in a
loop); and files read through other APIs from a path that is not a
literal of the test, such as `exec.Command`, `tls.LoadX509KeyPair`,
`http.ServeFile`, the `ParseFiles` method of a template, or any other
function, standard library included, that opens a path of its own.

### The go.module fragment

`aval gen go` also writes one fragment, `go.module`, with what every Go
test shares, and every generated test `use`s it:

```yaml
fragments:
  go.module:
    scope:
      dependencies:
        - go.mod
        - go.sum
    environment:
      toolchain: {go: 1.25.4}
      probe:
        go: [go, env, GOVERSION]
      env:
        - CGO_ENABLED
        - GOARCH
        - GODEBUG
        - GOEXPERIMENT
        - GOFLAGS
        - GOOS
        - GOTOOLCHAIN
        - GOWORK
```

- `toolchain.go` is the `toolchain` directive without its `go` prefix, else
  the `go` directive, of `go.work` in a workspace or of `go.mod`. The probe
  makes `aval run` check it against `go env GOVERSION` before the lookup, so
  the installed Go must match it exactly: with only `go 1.25.0`, a machine
  on go1.25.3 records and reuses nothing. Pin the patch version (a
  `toolchain` line, or `go` with the patch you install).
- `dependencies` are `go.work`, `go.work.sum`, and the `go.mod` and
  `go.sum` of every main module, when they exist, relative to the contract
  root.
- `env` are the variables that change what `go` builds and how it runs the
  tests.

It is owned like the other `go.*` entries, so a toolchain bump in `go.mod`
or a new `go.sum` is drift that `--check` reports. Do not repeat its
toolchain or probe in `defaults`: a different value there is a conflict,
and `aval gen` fails (exit 2). `aval gen go` fails and writes nothing
(exit 1) when it cannot read the version (a `go.mod` without a `go` directive, a
file that does not parse), finds no `go.mod` or `go.work`, or a module file
lies outside the contract root. A unit whose name would be `go.module` (a
package directory `module`, or a root package whose import path ends in
`/module`) is an error too.

```sh
aval gen go             # writes go.* into aval.yaml
aval gen go --go-cover  # each test also fails below 100% coverage of its package
```

`--go-cover` runs the tests through `sh -c` with `go test -coverprofile`
and fails when any statement of the package did not run, so a
repository-wide 100% gate becomes one gate per package. It needs `sh`,
`mktemp` and `awk`.

The package set and the embedded files are read as for `linux/amd64` with
cgo, with `GOFLAGS` cleared, so the output is the same on every machine. A
package with a file for another OS that uses `//go:embed` gets its whole
directory in scope. `aval gen go` fails and
writes nothing (exit 1) when `go` is missing, `go list` fails (no `go.mod`,
a broken import), a file does not parse, a package of a main module lies
outside the contract root, a `.go` file imports a package of a main module
that `go list` did not list (this includes a local package that builds
only for another OS, such as one with `//go:build windows`), or a dependency is replaced by a local
directory (its files are not in `go.sum`: add it to a `go.work` instead).

A test's scope follows the imports of the package's tests too, so a helper
package that only tests import is in the scope of every package that
depends on it. That re-runs more than needed, never less.

## Keep it up to date in CI

```sh
aval gen go --check
```

`--check` writes nothing. It exits 0 when the generated entries match the
graph, and 1 with a diff when they do not, such as a new import or a dropped
one. Run it in the same job as your linters.

## JavaScript and TypeScript

`aval gen js` reads the manifests and runs no tool. It makes one unit per
workspace package: the workspaces of `package.json` for npm and yarn,
`pnpm-workspace.yaml` for pnpm (`!` globs excluded). The root package is not
a unit. The lockfile at the contract root picks the package manager and the
test command:

| Lockfile | Command |
|---|---|
| `package-lock.json`, `npm-shrinkwrap.json` | `npm test --workspace <name>` |
| `pnpm-lock.yaml` | `pnpm --filter <name> test` |
| `yarn.lock` | `yarn workspace <name> test` |

A unit's ID is its package name and its files are its directory, so
`@acme/ui` in `packages/ui` becomes `js.acme.ui` with `packages/ui/**`. A
dependency becomes an edge when it names another workspace package (any
range, or `workspace:`), or when a `file:`, `link:` or `portal:` path points
to one. External packages are not edges: the lockfile in `defaults` covers
them. Exclude `node_modules` there too.

Two local dependencies that `package.json` does not name are edges as well:

- TypeScript configs. Every `tsconfig*.json` in a package (JSON with comments
  and trailing commas) adds an edge for each `references` path that lands in
  another workspace package. Its `extends` chain is followed the way
  TypeScript does (a relative path, a directory meaning its `tsconfig.json`,
  `.json` added when missing, or `<workspace package>/<path>`): a file in
  another workspace package is an edge, and a file outside every package,
  such as a root `tsconfig.base.json`, joins the unit's own files. `extends`
  of an external package is not followed.
- Relative imports. Every `.js`, `.jsx`, `.mjs`, `.cjs`, `.ts`, `.tsx`,
  `.mts`, `.cts`, `.vue`, `.svelte` and `.astro` file in a package is
  scanned for relative specifiers (`./`, `../`) of `import ... from`,
  `export ... from`, `import "..."`, `import(...)` and `require(...)`. One
  that lands in another workspace package is an edge. Comments are not
  stripped, so a commented-out import still counts.

The scan skips `node_modules`, `.git` and nested workspace packages;
hidden directories such as `.storybook` are scanned. It reads only those
references: `paths` aliases and `include` globs that reach outside the package are not edges, so declare
those by hand.

```yaml
apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults:
  scope:
    dependencies: [package.json, pnpm-lock.yaml, pnpm-workspace.yaml]
    exclude: ["**/node_modules/**"]
  cacheability: hermetic
fragments:
  js.acme.ui:
    scope:
      source:
        - packages/ui/**
  js.acme.utils:
    scope:
      source:
        - packages/utils/**
tests:
  - metadata: {name: js.acme.ui}
    runner: {command: [pnpm, --filter, '@acme/ui', test]}
    use:
      - js.acme.ui
      - js.acme.utils
  - metadata: {name: js.acme.utils}
    runner: {command: [pnpm, --filter, '@acme/utils', test]}
    use:
      - js.acme.utils
```

No lockfile or lockfiles of two package managers, an unreadable
`package.json` or `pnpm-workspace.yaml`, no workspaces, a workspace package
without a `name`, and a `workspace:` or `file:` reference to something that
is not a workspace package are errors: nothing is written, exit 1. So are a
relative import that lands outside every workspace package or outside the
contract root, or that is computed (a template literal with `${...}`),
named by file and line; a `references` path outside every workspace
package; and an unreadable tsconfig or a missing, absolute or out-of-root
`extends`. A package
without a `test` script still gets a test; its command fails until you add
the script.

## Python

`aval gen python` makes one unit per `pyproject.toml` with a project name
(`[project]` or `[tool.poetry]`): uv workspace members, poetry and pip
projects. The unit is the project name, PEP 503 normalized. It needs
Python 3.11 or later on `PATH` (`python3`, else `python`), which parses the
manifests with `tomllib` and the sources with `ast`.

```text
pyproject.toml          # acme, a uv workspace root and a project
uv.lock
src/acme/               # import acme_api
packages/core/          # acme-core
packages/api/           # acme-api, dependencies = ["acme-core"]
packages/cli/           # acme-cli, import acme_api, not declared
```

```yaml
tests:
  - metadata: {name: python.acme-cli}
    runner: {command: [uv, run, --package, acme-cli, pytest, packages/cli]}
    use:
      - python.acme-api
      - python.acme-cli
      - python.acme-core
fragments:
  python.acme-cli:
    scope:
      source:
        - packages/cli/**
```

- Edges: a local project named in `dependencies`, `optional-dependencies`,
  `dependency-groups`, `tool.uv.dev-dependencies` or the poetry dependency
  tables, or imported by a `.py` file of the project. External packages are
  not edges; put the lockfiles in `defaults`.
- Files: a project without nested projects owns `<dir>/**`, so a new file
  in it is in scope at once. The root project and any project with nested
  projects list their top-level files and directories instead, leaving out
  nested projects, hidden directories, `__pycache__`, `node_modules` and
  virtual environments (a directory with `pyvenv.cfg`): a new top-level file
  there needs `aval gen python` again, which `--check` catches. The root
  project also owns the files at the contract root, `aval.yaml` and lockfiles
  included, so an edit to them re-runs its test. Add
  `exclude: ["**/__pycache__/**", "**/.venv/**"]` to `defaults` so bytecode
  and virtual environments never change a fingerprint.
- Command: `uv run --package <name> pytest <dir>` when `uv.lock` sits at the
  contract root, otherwise `python -m pytest <dir>` in the active
  environment (poetry projects too). Nested projects are passed as
  `--ignore`.
- Imports map to the packages (a directory with `__init__.py`) and modules
  at the top of a project and of its `src/`. Relative imports stay in the
  project. Not seen: namespace packages, custom `package-dir` layouts, and
  modules loaded by path (`runpy`, `exec`, `spec_from_file_location`).
- `import_module("x")` and `__import__("x")` with a string count as
  imports. With `--strict-imports`, a call whose module is not a string
  literal is an error that names the file and line; without it, it is
  ignored.

No Python 3.11+, an unreadable or invalid `pyproject.toml` or a `.py` syntax
error: nothing is written, exit 1.

## Rust

`aval gen rust` reads the Cargo workspace found from the contract root with
`cargo metadata --no-deps` (no network, no build scripts). Each workspace
member is a unit named after its package: its scope is the member's
directory, its test is `cargo test -p <member>`, and its path dependencies
on other members (normal, dev and build) are its edges. Registry and git
dependencies are not units: put the root `Cargo.toml` (workspace
inheritance) and `Cargo.lock` in `defaults`.

```yaml
apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults:
  scope:
    dependencies: [Cargo.toml, Cargo.lock]
  cacheability: hermetic
fragments:
  rust.domain:
    scope:
      source:
        - crates/domain/**
  rust.store:
    scope:
      source:
        - crates/store/**
tests:
  - metadata: {name: rust.domain}
    runner: {command: [cargo, test, -p, domain]}
    use:
      - rust.domain
  - metadata: {name: rust.store}
    runner: {command: [cargo, test, -p, store]}
    use:
      - rust.domain
      - rust.store
```

- A path dependency on a crate that is not a workspace member, or a member
  outside the contract root, is an error: its files would be in no scope.
  Paths are compared after resolving symlinks, so a root reached through one
  (a clone under macOS `/tmp`) works.
- A dev-dependency is an edge like any other, so the tests of a member's
  dependents use its dev-dependencies too: more re-runs, never a stale reuse.
- A package at the workspace root gets `**`: add `target/**` to
  `defaults.scope.exclude`.
- `cargo metadata` does not show path overrides, so `aval gen` reads them
  from the workspace root: `path` entries of `[patch]` tables in
  `Cargo.toml` and `.cargo/config.toml`, and `paths` in
  `.cargo/config.toml`. An override inside a member makes every member
  depend on that member. One outside every member, or one it cannot read,
  is an error naming the file. `.cargo/config.toml` files in parent
  directories or in `CARGO_HOME` are not read: keep overrides at the
  workspace root.

## .NET

`aval gen dotnet` reads the solution and project files themselves, so it
needs no .NET SDK. Its units are the projects of the `.sln` or `.slnx` at the
contract root, or every `.csproj`, `.fsproj` and `.vbproj` under it when
there is no solution (skipping hidden, `bin`, `obj` and `node_modules`
directories), plus every project they reference. A unit's ID is its project
name, and its test is `dotnet test <project>`. An excerpt; the full output is
in `internal/gen/dotnet/testdata/golden.yaml`.

```yaml
fragments:
  dotnet.shop.api:
    scope:
      source:
        - Directory.Build.props
        - shared/Version.cs     # <Compile Include="..\..\shared\Version.cs" />
        - src/Shop.Api/**
tests:
  - metadata: {name: dotnet.shop.api}
    runner: {command: [dotnet, test, src/Shop.Api/Shop.Api.csproj]}
    use:
      - dotnet.shop.api
      - dotnet.shop.core        # through Shop.Data
      - dotnet.shop.data        # <ProjectReference Include="..\Shop.Data\Shop.Data.csproj" />
      - dotnet.shop.legacy      # referenced, not in the .sln
```

- A unit's files are its project directory, the `Directory.Build.props`,
  `Directory.Build.targets` and `Directory.Packages.props` files from there
  up to the root, the files its `Import`s name, and the items it includes
  from outside its directory, such as a shared `Compile` or `Protobuf` glob.
  Imported files are read too, so a `ProjectReference` in a
  `Directory.Build.props` counts.
- Only `ProjectReference`s become dependencies. Keep `global.json`,
  `NuGet.config` and `packages.lock.json` files in `defaults`.
- Fails closed: a malformed file, a missing referenced project, a path
  outside the repository or built from an MSBuild property other than
  `$(MSBuildThisFileDirectory)` or `$(MSBuildProjectDirectory)`, a wildcard
  `Import`, a `GetPathOfFileAbove` of a file other than the `Directory.*`
  files or from outside the project's directory and those above it, several
  solutions at the root, or two projects with the same name is an error.
- Files that only a property names (such as a `KeyOriginatorFile`) are not
  followed: add them to `defaults`.

## Java and Kotlin

`aval gen java` reads the build files at the contract root and runs no JVM
tool. A unit's files are its whole directory (`<module>/**`) plus the root
context: each top-level file and directory of the root that is not a module
directory or build output (`.git`, `.gradle`, `build`, `out`, `target`),
since build files read root files such as `config/checkstyle.xml`. A
directory that holds modules is included whole. Exclude each build output
directory in `defaults` by hand: the root `target/**`, `build/**` and
`.gradle/**`, plus `<module>/target/**` or `<module>/build/**` per module,
such as `app/build/**`. Do not write `**/build/**` or `**/target/**`: they
also hide source packages named `build` or `target`, and a change there
would be reused.

- Maven: one unit per module in `<modules>` (Maven 4 `<subprojects>`), in
  the root POM, nested aggregators and every profile. A module depends on
  every local module its POM names with a `groupId` and `artifactId`:
  `<parent>`, dependencies, plugins and `pluginManagement`, extensions,
  imported BOMs or plugin configuration such as
  `annotationProcessorPaths`, or a `groupId:artifactId` string (a
  `resourceBundle`, `dependenciesToScan`), and on what its local parents
  and the root POM name, resolved with its own properties. Managed dependencies and
  `exclusions` are not edges. The test is `mvn -B -pl <module> test`
  (`./mvnw` when the root has it), which takes the other modules from the
  local repository: run `mvn -B install -DskipTests` before `aval run`.
- Gradle: one unit per `include` in `settings.gradle.kts` or
  `settings.gradle`. A subproject depends on its parent projects and on the
  projects named in `project(...)`, a type-safe accessor
  (`projects.feature.ui`), `evaluationDependsOn(...)` or a task path such
  as `":common:jar"` by its build script, the settings script, the root
  build script, `buildSrc`, an `includeBuild` of a top-level directory
  (`build-logic`) and its parent projects' build scripts. The test is
  `./gradlew :<path>:test` (`gradle` without a wrapper).

Neither adapter sees test code that reads another module's files at run
time: declare those by hand in a fragment.

```yaml
defaults:
  scope:
    exclude: [.gradle/**, build/**, common/build/**, inventory/build/**]
  cacheability: hermetic
fragments:
  java.common:
    scope:
      source:
        - build.gradle.kts      # root context
        - common/**
        - gradle/**             # root context
        - gradlew               # root context
        - services/**           # holds projects, not one itself
        - settings.gradle.kts   # root context
```

What it cannot read for certain is an error (exit 1), not a guess:

- a computed `include`, `project(...)` or `evaluationDependsOn(...)` path,
  an accessor that names no included project, an `includeBuild` of
  anything but a top-level directory that is not a project, `projectDir`,
  `buildFileName`, `apply from` or a triple-quoted string;
- in Gradle, `parent`, `getParent`, `parentFile`, or
  `rootProject.subprojects` and `project(":").subprojects` (and kin) in a
  subproject; in any script, `resolveSibling`, an absolute path given to a
  path-taking call (`file`, `files`, `File`, `dir`, `resolve`, ...), a
  string with `../` or `rootDir` inside larger text (`"-Dfx=../common"`),
  a `resolve` chain on `rootDir` with a computed argument, or a path that
  leaves the root or starts with another project's directory
  (`rootProject.file("common/fixtures")`, `file("common")`,
  `rootDir.resolve("services").resolve("api")`);
- in Maven, an element or property value with `../` or `..\` anywhere (an
  `argLine` too), an absolute path, a path that starts with another
  module's directory (`config/` is fine, `${maven.multiModuleProjectDirectory}/`
  and `${rootDirectory}/` are read from the root), or `${env.*}`,
  `${session.*}`, `${project.parent.*}` or a property set at run time
  (`${main.basedir}`) next to a `/`;
- a Maven property it cannot resolve in a coordinate, or that a profile,
  `.mvn/maven.config` or `.mvn/jvm.config` sets;
- a reference to a local POM that is not a module, or a local `<parent>`
  that is neither the root POM nor a module;
- a root with both a POM and a Gradle settings script.

## Cross-language dependencies

A generator sees one ecosystem. Declare a dependency across languages by
hand only when no shared file carries it. When a Go binary embeds the
frontend build with `go:embed`, or a client SDK is generated into the tree,
those files already sit in a unit's scope. Otherwise add the other side's
fragment to a hand-written test's `use`, or its globs to `defaults`.

## Fail closed

A missing language tool, a parse error, a dependency on an unknown unit or a
unit without files is an error, and nothing is written. Exit codes: 0 written
or up to date, 1 error or `--check` out of date, 2 usage (an unknown
language, no `kind: TestSet` at `-f`, a `--no-test` pattern that matches no
unit, or a result that is not a valid TestSet).
