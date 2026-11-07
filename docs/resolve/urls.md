# Downloaded files

`url: <url>` downloads the file and hashes its bytes (sha256), up to
256 MiB.

```yaml
scope:
  resolve:
    - url: https://github.com/acme/tool/releases/latest/download/tool-linux-amd64.tar.gz
    - url: https://example.com/install.sh
```

- A `curl URL | sh`, a `sh -c "$(curl URL)"` or a `releases/latest`
  download the [scanner](../pinning.md#scripts-from-the-network) reports
  still blocks reuse with a `url` entry: `scope.resolve` never clears a
  finding. Vendor the script or check its sha256, or accept it with
  `scope.allowMutable`. The `url` entry hashes the bytes, so new content
  changes the fingerprint.
- Also covers what the scanner does not detect: a `curl -o tool URL` or
  `curl URL | tar x` of a URL without a version. The same bytes under
  another URL give the same id.
- `https` or `http` only. Credentials: `~/.netrc`. The reference is
  published in the manifest, so a URL with a credential-like query
  parameter (`token`, `private_token`, `sig`, `X-Amz-Signature`, ...) is
  rejected; a presigned URL cannot be resolved.
- Aval downloads the file every time it fingerprints, and the test
  downloads it again: a change between the two is not seen.

**Pin instead** with a versioned URL and a checksum the test checks
(`sha256sum -c`), or vendor the file into the scope.
