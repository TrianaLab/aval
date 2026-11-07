# Go modules

`go: <module>@<query>` hashes the module zip the way `go.sum` does
(`h1:`).

What a query binds depends on its form, as the go command reads it:

- `vX.Y.Z`, with an optional `-pre` (pseudo-versions too), is exact: it
  binds that version's zip.
- A branch, a commit or any other query that is not a semver version
  (`main`, `4f3c2a1`) is resolved by the proxy (`/@v/<query>.info`, as the
  go command does) to the version it names, usually a pseudo-version, and
  that version's zip is bound. A moved branch changes the id; the manifest
  line names the resolved version.
- `latest`, a prefix (`v1.2`), a comparison (`<v1.3.0`) or a
  `+incompatible` version binds the proxy's version list, its `@latest`
  and the `h1:` hash of the version `@latest` names. A new version, a
  retraction or a republished `@latest` version changes the id. Other
  versions are bound by name: the checksum database keeps every version
  the go command accepts fixed. The manifest line says `floating`. No
  query means `latest`.

```yaml
scope:
  resolve:
    - go: golang.org/x/tools@v0.26.0   # exact: binds v0.26.0
    - go: github.com/acme/lib@main     # a branch: binds its current commit
    - go: golang.org/x/net@latest      # floating: binds the version list
```

- The module path must be a valid module path (`golang.org/x/mod`'s
  `module.CheckPath`): a relative or absolute path, a Windows path or a
  first element that is not a lowercase domain fails.
- The module path is the module, not a package inside it:
  `golang.org/x/tools`, unless the package is its own module.
- Proxy: the first entry of `$GOPROXY` (`https://proxy.golang.org`).
  Credentials: `~/.netrc`.
- Resolution fails when the go command would fetch the module directly
  instead of from that proxy: the first `$GOPROXY` entry is `direct` or
  `off`, or `$GOPRIVATE` or `$GONOPROXY` matches the module (the go
  command's glob prefix rules). Aval never clones: pin the module or use
  the `git` kind.
- Resolution also fails when the go command would not verify the module
  against the checksum database: `GOSUMDB=off`, `$GONOSUMDB` or
  `$GOINSECURE` matches the module, or `$GOFLAGS` holds `-insecure`. A
  proxy could then serve other content for a version than the one Aval
  hashed. Settings saved with `go env -w` are not read.

**Pin instead** for your module's dependencies: commit `go.sum` and add it
to `scope.dependencies`. Resolve is for `go install tool@v1.2.3` in
`runner.command`. The scanner still reports that install, so accept it
with `scope.allowMutable`.
