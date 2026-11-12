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
    JavaScript and TypeScript ([below](#javascript-and-typescript)), Python
    ([below](#python)) and Rust ([below](#rust)) ship. The other adapters (Go,
    Java, .NET) are the next stories of
    [Epic 16](https://github.com/TrianaLab/aval/issues/291). `aval gen --help`
    lists the languages your build has.

## What it writes

Given units `a` and `b` where `a` imports `b`, `aval gen go` adds:

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

## Ownership

`aval gen go` owns the fragments and tests named `go.*` and nothing else:

- It replaces those entries in place (the first one's position, or the end
  of `fragments` and `tests`) and keeps every other line byte for byte,
  comments and blank lines included. Comment lines right above a later
  generated entry go with it, and a missing final newline is added.
- The prefix is reserved: a hand-written `go.*` entry is replaced.
- Two generators share a file: `aval gen js` owns `js.*` and leaves `go.*`
  alone.
- `fragments` and `tests` must be block style (not `{}` or `[]`).

Shared inputs stay hand-written. Put lockfiles, root build files, the
toolchain, probes, `env` and `cacheability` in `defaults` or in your own
fragments; every generated test inherits `defaults`.

```yaml
apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults:
  scope:
    dependencies: [go.mod, go.sum]
  environment:
    toolchain: {go: "1.25"}
  cacheability: hermetic
fragments:
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
      - go.web
```

A hand-written test can `use` generated fragments. When the unit behind one
disappears, `aval gen` fails (exit 2) and writes nothing.

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
language, no `kind: TestSet` at `-f`, or a result that is not a valid
TestSet).
