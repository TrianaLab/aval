# Resolve references

The fingerprint hashes the text of your files. `postgres:16` is hashed as
the words `postgres:16`, not as the image behind them. [Pinning](../pinning.md)
fixes that by writing the immutable id into a file: a digest, a lockfile, a
commit. When you cannot or do not want to pin, declare the reference in
`scope.resolve`: Aval looks up what the name points to every time it
fingerprints, and hashes that id.

```yaml
apiVersion: aval.trianalab.io/v1
kind: Test
metadata:
  name: orders-integration
runner:
  command: ["docker", "compose", "run", "--rm", "tests"]
scope:
  source: ["**/*.go", "compose.yaml"]
  resolve:
    - image: postgres:16
    - npm: prettier@^3
    - git: https://github.com/acme/fixtures@main
cacheability: controlled-environment
```

When someone repushes `postgres:16`, publishes any new `prettier` release
or moves `main`, the id changes, the fingerprint changes, and Aval runs the
test instead of reusing an old result.

## Exact or floating

<!-- --8<-- [start:floating] -->

An exact version binds that release. Anything else (a range, `latest`, a tag)
binds the whole release list, so any new release runs the test again. Aval
never picks a version from a range.

<!-- --8<-- [end:floating] -->

| Kind | Exact | Floating hashes |
|---|---|---|
| `npm` | `x.y.z[-pre]` | every version's `dist.integrity` and deprecation, every dist-tag |
| `pypi` | one `==` without `*` | every file's sha256 and yanked flag |
| `crate` | `=x.y.z` | every index entry's checksum and yanked flag |
| `gem` | a plain version | every version and platform checksum |
| `maven` | a plain version | every listed version's POM and checksums (100 versions at most) |
| `go` | `vX.Y.Z` (a branch or commit resolves to a version) | the version list, plus `@latest` and its `h1:` hash, for `latest`, a prefix, a comparison or `+incompatible` |
| `image` | | the tag's manifest digest |
| `git` | a full commit | the commit a branch or tag points to |
| `url` | | the bytes |

A publish, a yank or a moved tag changes the id: the test runs more often,
never on stale inputs. When in doubt, a spec is floating. A spec the tool
installs from elsewhere (`npm:x`, `github:`, `file:`, `./dir`) or a name
outside the ecosystem's rules fails: use `git` or `url`, or pin. Each page
lists the rules.

## Pin or resolve?

| Pin (digest, lockfile, commit) | Resolve (`scope.resolve`) |
|---|---|
| Works offline; the fingerprint needs no network. | Needs the registry every time Aval fingerprints. |
| You update the pin by hand or with a bot. | You follow a tag or a branch on purpose; a range follows every release. |
| What you run is what you hashed. | The id is read when Aval fingerprints; a push between that and your test's own download is not seen. |

Prefer pinning. Use `scope.resolve` for references you mean to follow:
a nightly base image, a tool at `latest`, a fixtures branch.

## How it works

- The manifest gets one line per reference, after the `env` lines:
  `resolve <kind> <reference> <version> <id>`, where `<version>` is
  `floating` for a reference that names no exact version. `aval run
  --explain` shows them, and prints on stderr how long each lookup took,
  including those of the contracts in `scope.contracts`.
- The list is part of the contract hash; its order and duplicates do not
  matter. Contracts without `scope.resolve` never touch the network.
- `scope.resolve` binds the artifact you name, not its dependencies. npm,
  pip, `cargo install` without `--locked` and `gem install` still pick
  transitive versions when they install. Pin those with a lockfile.
- No cache: every fingerprint asks the registry again.

## scope.resolve and the scanner

<!-- --8<-- [start:scanner] -->

`scope.resolve` never clears a [scanner](../pinning.md) finding: a move
changes the fingerprint, but the reference still blocks reuse. Pin it in
place, or accept it with
[`scope.allowMutable`](../pinning.md#accept-a-reference-on-purpose).

<!-- --8<-- [end:scanner] -->

## When resolution fails

A reference that cannot be resolved (network, credentials, an exact
version that is not published, a listed version without a checksum, a
download over 256 MiB, the 2-minute deadline) fails the fingerprint.
Nothing is looked up, the test runs, and the reason names the reference:
`fingerprint: resolve npm left@9.9.9: version 9.9.9 is not published`.
`aval verify` exits 1; `aval run --push` runs the test and publishes nothing.

## Registries and credentials

| Kind | Registry (default) | Credentials |
|---|---|---|
| `image` | the host in the reference (Docker Hub) | Docker config (`docker login`) |
| `npm` | `$npm_config_registry` (registry.npmjs.org) | `//host/:_authToken=` in `~/.npmrc`, else `~/.netrc` |
| `pypi` | `$AVAL_PYPI_URL` (pypi.org, JSON API) | `~/.netrc` |
| `go` | first proxy in `$GOPROXY` (proxy.golang.org) | `~/.netrc` |
| `maven` | `$AVAL_MAVEN_URL` (repo.maven.apache.org/maven2) | `~/.netrc` |
| `crate` | `$AVAL_CRATES_INDEX` (index.crates.io, sparse) | `~/.netrc` |
| `gem` | `$AVAL_RUBYGEMS_URL` (rubygems.org) | `~/.netrc` |
| `git` | the URL in the reference | git's own (credential helpers, SSH keys) |
| `url` | the URL in the reference | `~/.netrc` |

- Only `machine` entries of `~/.netrc` (or `$NETRC`) are used, never
  `default`, and only over `https` or to a loopback host (also after a
  redirect). `$npm_config_userconfig` (any letter case) points npm's file
  elsewhere. Credentials go in headers, never in the manifest, a URL or an
  error; a user, password or path in a registry variable is masked in errors
  and refused over plain `http` to a host that is not loopback.
- A reference with credentials is rejected without echoing it: a password in
  any URL, a user in an `http(s)` or scheme-less `//user@host` URL (an
  `ssh://git@host` user is fine), an `@` in the authority of a URL that does
  not parse, or a query parameter named `token`, `private_token`,
  `access_token`, `key`, `apikey`, `api_key`, `sig`, `signature`,
  `X-Amz-Signature`, `X-Goog-Signature`, `auth`, `password`, `secret`,
  `credential`, `security_token`, `x_amz_security_token`, `client_secret`,
  `session` or `sessionid` (any case, `-` read as `_`, a trailing `[]`
  ignored).
- The registry address is not hashed: the same content on a mirror resolves
  to the same id. Requests carry the User-Agent
  `aval/<version> (+https://github.com/TrianaLab/aval)`.

The resolved id vouches for the registry in the table, not for the one the
tool installs from. Where the tool's own setting points elsewhere, resolution
fails:

| Kind | Fails when |
|---|---|
| `npm` | `~/.npmrc` has a `@scope:registry` or `registry` line, or an `npm_config_@scope:registry` variable is set, that differs from Aval's registry; two spellings of an `npm_config_*` variable differ; a `~/.npmrc` line cannot be read |
| `go` | `$GOPROXY` starts with `direct` or `off`; `$GOPRIVATE` or `$GONOPROXY` matches; `GOSUMDB=off`, `$GONOSUMDB`, `$GOINSECURE` or `-insecure` in `$GOFLAGS` |
| `pypi` | `$PIP_INDEX_URL` or `$PIP_PYPI_URL` names another index; `$PIP_EXTRA_INDEX_URL` or `$PIP_FIND_LINKS` is set; `$PIP_NO_INDEX` is true (any spelling pip reads) |
| `maven` | `<latest>` or `<release>` is a SNAPSHOT or not listed |

Not read: `pip.conf`, a project `.npmrc`, `go env -w` settings, Cargo's
`config.toml`, Maven's `settings.xml`, `.gemrc`. If one of them points the
tool to another registry, set the Aval variable to the same registry.

## Read next

- [Pin your dependencies](../pinning.md)
- [The test contract](../contract.md#what-goes-into-the-fingerprint)
