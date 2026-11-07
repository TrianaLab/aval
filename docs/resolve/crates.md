# Rust crates

`crate: <name>@=<version>` hashes the `.crate` checksum of that version
from the crates.io index.

An exact version binds that version. Anything else (a range, latest, a
tag) binds the whole release list, so any new release of the package
makes the test run again; use an exact version (or a lockfile) for stable
reuse.

```yaml
scope:
  resolve:
    - crate: cargo-nextest@=0.9.81   # exact
    - crate: serde@1                 # floating (Cargo reads it as ^1)
    - crate: tokio                   # floating
```

- Exact means Cargo's `=x.y.z`. A bare `1.2.3` means `^1.2.3` to Cargo,
  so it is floating. A yanked version is not resolved: Cargo does not pick
  it either.
- A spec with `:`, `/` or `#`, or starting with `.` or `~/`, is not a
  registry requirement and fails: use the `git` or `url` kind, or pin.
- The name must be a crates.io name: a letter, then up to 63 letters,
  digits, `_` or `-`. Anything else fails.
- For anything else the id is a hash of every index entry: version,
  checksum and yanked flag. A new version, a yank or an unyank changes the
  id. The manifest line says `floating`.
- Index: `$AVAL_CRATES_INDEX` (the sparse index protocol). Credentials:
  `~/.netrc`.

**Pin instead** for your crate's dependencies: commit `Cargo.lock` and add
it to `scope.dependencies`. Resolve is for `cargo install tool` in
`runner.command`. The scanner still reports that install, so accept it
with `scope.allowMutable`. Only the crate is bound: without `--locked`,
`cargo install` picks new versions of its dependencies.
