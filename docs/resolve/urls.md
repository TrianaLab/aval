# Downloaded files

`url: <url>` downloads the file and hashes its bytes (sha256), up to
256 MiB.

```yaml
scope:
  resolve:
    - url: https://github.com/acme/tool/releases/latest/download/tool-linux-amd64.tar.gz
    - url: https://example.com/install.sh
```

- Also covers what the scanner does not detect: a `curl -o tool URL` or
  `curl URL | tar x` of a URL without a version. The same bytes under
  another URL give the same id.
- `https` or `http` only. Credentials: `~/.netrc`. A presigned URL is
  rejected ([why](index.md#registries-and-credentials)).
- Aval downloads the file every time it fingerprints, and the test
  downloads it again: a change between the two is not seen.

--8<-- "docs/resolve/index.md:scanner"

**Pin instead** with a versioned URL and a checksum the test checks
(`sha256sum -c`), or vendor the file into the scope.
