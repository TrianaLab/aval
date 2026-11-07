# Go modules

`go: <module>@<query>` hashes the module zip the way `go.sum` does
(`h1:`). What a query binds depends on its form, as the go command reads it:

| Query | Binds |
|---|---|
| `vX.Y.Z`, optional `-pre` (pseudo-versions too) | that version's zip (exact) |
| a branch, a commit or another non-semver query (`main`, `4f3c2a1`) | the zip of the version the proxy resolves it to, usually a pseudo-version; the manifest line names it |
| `latest` (or no query), a prefix (`v1.2`), a comparison (`<v1.3.0`), `+incompatible` | the proxy's version list, its `@latest` and that version's `h1:` hash (`floating`) |

```yaml
scope:
  resolve:
    - go: golang.org/x/tools@v0.26.0   # exact: binds v0.26.0
    - go: github.com/acme/lib@main     # a branch: binds its current commit
    - go: golang.org/x/net@latest      # floating: binds the version list
```

- For a floating query, other versions are bound by name: the checksum
  database keeps every version the go command accepts fixed.
- The module path must pass `golang.org/x/mod`'s `module.CheckPath`, and
  names the module, not a package inside it.
- Proxy: the first entry of `$GOPROXY` (`https://proxy.golang.org`);
  credentials from `~/.netrc`.
- Fails when the go command would bypass the proxy or the checksum database:
  `$GOPROXY` starting with `direct` or `off`, `$GOPRIVATE` or `$GONOPROXY`
  matching, `GOSUMDB=off`, `$GONOSUMDB` or `$GOINSECURE` matching, or
  `-insecure` in `$GOFLAGS`. `go env -w` settings are not read. Aval never
  clones: pin the module or use `git`.

**Pin instead** for your module's dependencies: commit `go.sum` and add it
to `scope.dependencies`. Resolve is for `go install tool@v1.2.3` in
`runner.command`.

--8<-- "docs/resolve/index.md:scanner"
