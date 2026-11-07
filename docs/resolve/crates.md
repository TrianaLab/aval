# Rust crates

`crate: <name>@=<version>` hashes the `.crate` checksum of that version
from the crates.io index.

--8<-- "docs/resolve/index.md:floating"

```yaml
scope:
  resolve:
    - crate: cargo-nextest@=0.9.81   # exact
    - crate: serde@1                 # floating (Cargo reads it as ^1)
    - crate: tokio                   # floating
```

- Exact means Cargo's `=x.y.z`; a bare `1.2.3` means `^1.2.3`, so it is
  floating. A yanked version is not resolved: Cargo does not pick it either.
- The name is a crates.io name (a letter, then up to 63 letters, digits, `_`
  or `-`). A spec with `:`, `/` or `#`, or starting with `.` or `~/`, fails:
  use `git` or `url`, or pin.
- Floating: every index entry with its checksum and yanked flag.
- Index `$AVAL_CRATES_INDEX` (sparse protocol); credentials from `~/.netrc`.

**Pin instead** for your crate's dependencies: commit `Cargo.lock` and add
it to `scope.dependencies`. Resolve is for `cargo install tool` in
`runner.command`. Only the crate is bound: without `--locked`,
`cargo install` picks new versions of its dependencies.

--8<-- "docs/resolve/index.md:scanner"
