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
    JavaScript, TypeScript, Java and Kotlin ship (`aval gen js` and
    `aval gen java`, below). The other adapters (Go, Python, Rust, .NET)
    are the next stories of
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
them. Exclude `node_modules` there too:

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
is not a workspace package are errors: nothing is written, exit 1. A package
without a `test` script still gets a test; its command fails until you add
the script.

## Java and Kotlin

`aval gen java` reads the build files at the contract root and runs no JVM
tool:

- Maven: one unit per module in `<modules>` (Maven 4 `<subprojects>`), in
  the root POM, nested aggregators and every profile. A module depends on
  the local modules it names as `<parent>`, in `<dependencies>` or as a
  build plugin; `dependencyManagement` and `pluginManagement` declare
  versions, not edges. The test is `mvn -B -pl <module> test` (`./mvnw` when
  the root has it), which tests that module only and takes the other
  modules from the local repository: run `mvn -B install -DskipTests`
  before `aval run`.
- Gradle: one unit per `include` in `settings.gradle.kts` or
  `settings.gradle`. A subproject depends on the `project(":path")`
  references of its build script; a reference in the root build script
  (`subprojects { ... }`) becomes an edge of every subproject. The build
  script of an intermediate project (`services` for `:services:web`) is an
  input of the subprojects under it, and its references are their edges.
  The test is `./gradlew :<path>:test` (`gradle` without a wrapper).

A unit's files are its `pom.xml` or `build.gradle(.kts)`, a Gradle
subproject's `gradle.properties` and `src/**`. Other files in the module
directory, such as `lombok.config`, go in a hand-written fragment. The root
project is not a unit; its build files and whatever else configures every
module (`buildSrc/**`, `gradle.properties`, `gradle/wrapper/**`, version
catalogs, `.mvn/**`) go in `defaults`:

```yaml
apiVersion: aval.trianalab.io/v1
kind: TestSet
defaults:
  scope:
    dependencies: [settings.gradle.kts, build.gradle.kts, gradle/libs.versions.toml]
  cacheability: hermetic
fragments:
  java.common:
    scope:
      source:
        - common/build.gradle.kts
        - common/src/**
  java.inventory:
    scope:
      source:
        - inventory/build.gradle.kts
        - inventory/src/**
tests:
  - metadata: {name: java.common}
    runner: {command: [./gradlew, ':common:test']}
    use:
      - java.common
  - metadata: {name: java.inventory}
    runner: {command: [./gradlew, ':inventory:test']}
    use:
      - java.common
      - java.inventory
```

What it cannot read for certain is an error (exit 1), not a guess: a
computed `include` or `project(...)` path, type-safe accessors
(`projects.common`), `includeBuild`, `projectDir`, `buildFileName`, a
source or resource directory outside `src` (a Gradle `srcDir` that is not a
string literal under `src`), a local `<parent>` POM that is neither the root
POM nor a module, a property it cannot resolve in
a local module's coordinates, or a root with both a POM and a Gradle
settings script.

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
