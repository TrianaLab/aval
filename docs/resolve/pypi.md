# Python packages (PyPI)

`pypi: <name>==<version>` hashes the sha256 of every file of that release
(sdist and wheels).

--8<-- "docs/resolve/index.md:floating"

```yaml
scope:
  resolve:
    - pypi: ruff==0.6.9        # exact: binds 0.6.9
    - pypi: requests==2.32.*   # floating: binds every release
    - pypi: pytest             # floating
```

- Exact means one `==` clause with no `*`. Extras (`requests[socks]`) are
  ignored; environment markers are not supported.
- `==X` binds every release pip would match for it, including local labels
  (`X+cu121`) and zero-padded forms (`X.0`); `==X+label` binds only that
  label. A wheel added to the release later changes the id.
- Floating: every file of every release, with its sha256 and yanked flag.
- The name must be a PEP 508 name, with optional `[extra,...]`. A path, a
  URL, a marker or an archive file name (`.whl`, `.tar.gz`, `.zip`, ...)
  fails: pip would install it from disk or elsewhere. Use `url`.
- Registry `$AVAL_PYPI_URL` (an index with the PyPI JSON API at
  `/pypi/<name>/json`); credentials from `~/.netrc`. `PIP_*` variables that
  point pip elsewhere fail ([details](index.md#registries-and-credentials));
  `pip.conf` and `uv` settings are not read.

**Pin instead** for your requirements: `pip-compile --generate-hashes` or
`uv pip compile --generate-hashes`, commit the file, and add it to the
scope. Resolve is for a tool the test installs on its own, such as
`pip install ruff==0.6.9`. Only the package is bound, not the dependencies
pip picks for it.

--8<-- "docs/resolve/index.md:scanner"
