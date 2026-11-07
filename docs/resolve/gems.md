# Ruby gems

`gem: <name>@<version>` hashes the checksums of all platform gems of that
version.

--8<-- "docs/resolve/index.md:floating"

```yaml
scope:
  resolve:
    - gem: rubocop@1.66.1   # exact
    - gem: rails@~>7.1      # floating
    - gem: bundler          # floating
```

- Exact means a plain version (`7.1.3`, `7.2.0.beta1`) with no operator. It
  binds every number RubyGems holds equal to it (`7.1.3.0` too).
- The name is a RubyGems name (a letter or digit, then letters, digits, `.`,
  `_` or `-`). A spec with `:`, `/` or `#`, starting with `.` or `~/`, or a
  name ending in `.gem` fails: use `git` or `url`, or pin.
- `gem install` also reads a matching `<name>-<version>.gem` file in the
  current directory, which Aval does not see: run it where no such file lies.
- Floating: every version, platform and checksum.
- Registry `$AVAL_RUBYGEMS_URL`; credentials from `~/.netrc`.

**Pin instead** for your application's gems: commit `Gemfile.lock` and add
it to `scope.dependencies`. Resolve is for `gem install tool` in
`runner.command`. Only the gem is bound, not the dependencies `gem install`
picks.

--8<-- "docs/resolve/index.md:scanner"
