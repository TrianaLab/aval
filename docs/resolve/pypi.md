# Python packages (PyPI)

`pypi: <name>==<version>` hashes the sha256 of every file of that release
(sdist and wheels).

An exact version binds that version. Anything else (a range, latest, a
tag) binds the whole release list, so any new release of the package
makes the test run again; use an exact version (or a lockfile) for stable
reuse.

```yaml
scope:
  resolve:
    - pypi: ruff==0.6.9        # exact: binds 0.6.9
    - pypi: requests==2.32.*   # floating: binds every release
    - pypi: pytest             # floating
```

- Exact means one `==` clause with no `*`. Extras (`requests[socks]`) are
  ignored; environment markers are not supported.
- An exact `==X` binds every release pip matches for it: `X` with any
  local label (`X+cu121`, which pip prefers) and zero-padded forms
  (`X.0`), compared in lower case with leading zeros, a `v` and a `0!`
  epoch ignored. When `X` has a local label, only that local version is
  bound. Other spellings of a pre-release (`1.0c1` for `1.0rc1`) are not
  grouped.
- For anything else the id is a hash of every file of every release, with
  its sha256 and yanked flag. A new release, a yank or a new wheel changes
  the id. The manifest line says `floating`.
- A wheel added to an exact release later changes its id too.
- The name must be a PEP 508 name (`[A-Za-z0-9]`, then letters, digits,
  `.`, `_` or `-`, ending in a letter or digit), with optional
  `[extra,...]`; the specifier holds only version characters. A path, a
  URL, a marker or a name ending in an archive extension pip reads as a
  local file fails (`.whl`, `.tar.gz`, `.tgz`, `.zip`, `.tar`, `.egg`,
  `.tar.bz2`, `.tbz`, `.tar.xz`, `.txz`, `.tlz`, `.tar.lz`, `.tar.lzma`,
  with or without extras): pip installs it from disk or elsewhere. Use the
  `url` kind.
- Registry: `$AVAL_PYPI_URL` (an index with the PyPI JSON API at
  `/pypi/<name>/json`). Credentials: `~/.netrc`.
- If `$PIP_INDEX_URL` is set to anything but that registry (or its
  `/simple` index), or `$PIP_EXTRA_INDEX_URL` or `$PIP_FIND_LINKS` is set
  at all, or `$PIP_NO_INDEX` is true, resolution fails: pip would install
  from another source than the one Aval read. As in pip, the rest of the
  name after `PIP_` is read in any letter case, with `-` equal to `_`
  and one optional leading `--` (`PIP_index-url`, `PIP___INDEX_URL`);
  two spellings set to different values fail.
  `pip.conf` and `uv` settings are not read.

**Pin instead** for your requirements: `pip-compile --generate-hashes` or
`uv pip compile --generate-hashes`, commit the file, and add it to the
scope. Resolve is for a tool the test installs on its own, such as
`pip install ruff==0.6.9`. The scanner still reports that install, so
accept it with `scope.allowMutable`. Only the package is bound, not the
dependencies pip picks for it.
