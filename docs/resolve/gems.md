# Ruby gems

`gem: <name>@<version>` hashes the checksums of all platform gems of that
version.

An exact version binds that version. Anything else (a range, latest, a
tag) binds the whole release list, so any new release of the package
makes the test run again; use an exact version (or a lockfile) for stable
reuse.

```yaml
scope:
  resolve:
    - gem: rubocop@1.66.1   # exact
    - gem: rails@~>7.1      # floating
    - gem: bundler          # floating
```

- Exact means a plain version (`7.1.3`, `7.2.0.beta1`) with no operator.
  It binds every published number RubyGems holds equal to it (`7.1.3.0`
  too).
- A spec with `:`, `/` or `#`, or starting with `.` or `~/`, is not a
  registry requirement and fails: use the `git` or `url` kind, or pin.
- The name must be a RubyGems name: a letter or digit, then letters,
  digits, `.`, `_` or `-`. Anything else fails, and so does a name ending
  in `.gem`: gem installs it from disk. `gem install` also reads a
  matching `<name>-<version>.gem` file in the current directory, which
  Aval does not see: run it where no such file lies.
- For anything else the id is a hash of every version, platform and
  checksum. A new version or a yank changes the id. The manifest line says
  `floating`.
- Registry: `$AVAL_RUBYGEMS_URL`. Credentials: `~/.netrc`.

**Pin instead** for your application's gems: commit `Gemfile.lock` and add
it to `scope.dependencies`. Resolve is for `gem install tool` in
`runner.command`. The scanner still reports that install, so accept it
with `scope.allowMutable`. Only the gem is bound, not the dependencies
`gem install` picks.
