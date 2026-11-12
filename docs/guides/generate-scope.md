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
    .NET ships first ([below](#net)). The other adapters (Go, JavaScript,
    Python, Java, Rust) are the next stories of
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
