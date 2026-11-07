# npm packages

`npm: <name>@<version>` hashes the `dist.integrity` of that version.

--8<-- "docs/resolve/index.md:floating"

```yaml
scope:
  resolve:
    - npm: prettier@3.3.3      # exact: binds 3.3.3
    - npm: typescript@latest   # floating: binds every release and tag
    - npm: "@types/node@22.x"  # floating
```

- Exact means `x.y.z`, with an optional `-pre` and no operator. A name
  without a version means `latest`. A spec with `+build` is never found
  (the registry strips it) and fails.
- Fails for any spec npm installs from elsewhere: an alias (`npm:other`),
  `github:`, `org/repo`, `git+https:`, a tarball URL, `file:` or a path (any
  spec with `:`, `/`, `#` or `\`, starting with `.` or `~/`, or ending in
  `.tgz`, `.tar.gz` or `.tar`). Use `git` or `url`, or pin.
- The name follows npm's legacy rules, which npm still installs
  (`JSONStream`): at most 214 characters, an optional lowercase `@scope/`,
  then `[A-Za-z0-9-~][A-Za-z0-9-._~]*`. A name ending in `.tgz`, `.tar.gz`
  or `.tar` fails: npm reads it as a local file.
- Floating: every version with its `dist.integrity` and deprecation, and
  every dist-tag. A publish, an unpublish, a deprecation or a moved tag
  changes the id.
- Registry `$npm_config_registry`; token from the `//host/:_authToken=` line
  of `~/.npmrc` (or `$npm_config_userconfig`), `${VAR}` expanded. As in npm,
  `npm_config_*` variables are read in any letter case, with `_` equal to
  `-` after the first character. A project `.npmrc` and the global npmrc are
  not read.
- `~/.npmrc` is read as npm reads it (ini: quotes removed, `;` and `#`
  comments, keys under a `[section]` ignored). A line Aval cannot read as
  `key=value` fails the fingerprint, and no token is sent. Settings that
  point the package at another registry fail too
  ([details](index.md#registries-and-credentials)).

**Pin instead** for your project's own dependencies: commit
`package-lock.json` (or `yarn.lock`, `pnpm-lock.yaml`, `bun.lock`) and add
it to `scope.dependencies`. Resolve is for a tool the test installs on its
own, such as `npx prettier@3.3.3`. Only the package is bound, not the
dependencies npm picks for it.

--8<-- "docs/resolve/index.md:scanner"
