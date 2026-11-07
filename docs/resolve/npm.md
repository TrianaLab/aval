# npm packages

`npm: <name>@<version>` hashes the `dist.integrity` of that version.

An exact version binds that version. Anything else (a range, latest, a
tag) binds the whole release list, so any new release of the package
makes the test run again; use an exact version (or a lockfile) for stable
reuse.

```yaml
scope:
  resolve:
    - npm: prettier@3.3.3      # exact: binds 3.3.3
    - npm: typescript@latest   # floating: binds every release and tag
    - npm: "@types/node@22.x"  # floating
```

- Exact means `x.y.z`, with an optional `-pre` and no operator. A name
  without a version means `latest`. The registry strips `+build`
  metadata from its versions, so a spec with `+build` is never found and
  fails.
- A spec npm installs from somewhere else than the registry fails: an
  alias (`npm:other`), `github:`, `org/repo`, `git+https:`, a tarball URL,
  `file:` or a path (any spec with `:`, `/`, `#` or `\`, starting with
  `.` or `~/`, or ending in `.tgz`, `.tar.gz` or `.tar`, in any case). Use
  the `git` or `url` kind, or pin.
- The name must follow npm's rules for old packages, which npm still
  installs (`JSONStream`): at most 214 characters, an optional lowercase
  `@scope/`, then `[A-Za-z0-9-~][A-Za-z0-9-._~]*`. Anything else fails
  (`left-pad/`, `@types/node/`, `./x`, `@Types/node`), and so does a name
  ending in `.tgz`, `.tar.gz` or `.tar`: npm reads those as a local
  directory or file.
- For anything else the id is a hash of every version with its
  `dist.integrity` and deprecation, and every dist-tag. Aval does not pick
  a version from the range: a publish, an unpublish, a deprecation or a
  moved tag changes the id. The manifest line says `floating`.
- Registry: `$npm_config_registry`. Token: the `//host/:_authToken=` line
  of `~/.npmrc` (or `$npm_config_userconfig`), `${VAR}` expanded. As in
  npm, `npm_config_*` variables are read in any letter case; two
  spellings of one variable set to different values fail.
- If the environment or `~/.npmrc` points the package elsewhere,
  resolution fails instead of binding the wrong registry: a
  `npm_config_@scope:registry` variable for the package's scope, a
  `@scope:registry=` line for the package's scope, or a
  `registry=` line when no registry variable is set, that differs from the
  registry Aval uses. Set `$npm_config_registry` to the same URL. A
  project `.npmrc` and the global npmrc are not read.

**Pin instead** for your project's own dependencies: commit
`package-lock.json` (or `yarn.lock`, `pnpm-lock.yaml`, `bun.lock`) and add
it to `scope.dependencies`. Resolve is for a tool the test installs on its
own, such as `npx prettier@3.3.3`. The scanner still reports that install,
so accept it with `scope.allowMutable`. Only the package is bound, not the
dependencies npm picks for it.
