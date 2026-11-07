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

An exact version binds that version. For npm, PyPI, crates, gems and
Maven, anything else (a range, latest, a tag) binds the whole release
list, so any new release of the package makes the test run again; use an
exact version (or a lockfile) for stable reuse. Go differs: a `vX.Y.Z`
version is exact, a branch, a commit or another non-semver query is
resolved through the proxy to a version (usually a pseudo-version) whose
zip is bound, and only `latest`, a prefix (`v1.2`), a comparison or a
`+incompatible` version binds the version list
([Go modules](go.md)).

Aval never picks a version from a range. Each tool reads ranges its own
way, and a version Aval picked differently from the tool would be a false
hit. What a floating reference hashes:

- npm: every version with its `dist.integrity` and deprecation, and every
  dist-tag.
- PyPI: every file of every release with its sha256 and yanked flag.
- Crates: every index entry with its checksum and yanked flag.
- Gems: every version and platform with its checksum.
- Maven: every version in `maven-metadata.xml` with its POM and published
  checksum, plus `<latest>` and `<release>`; more than 100 versions fail.
- Go (`latest`, a prefix, a comparison, `+incompatible`): the proxy's
  version list and `@latest` with the `h1:` hash of the version it names;
  other versions by name (the checksum database keeps them fixed; a module
  the go command would not verify against it fails).

A publish, a yank or a moved tag changes the id: the test runs more
often, never on stale inputs. What counts as exact is on each ecosystem
page; when in doubt, a spec is floating. A spec the tool installs from
somewhere else than the registry (an npm alias `npm:x`, `github:`,
`org/repo`, `git+https:`, `file:`, `./dir`) fails: use the `git` or `url`
kind, or pin. A package name must match its ecosystem's naming rules
(npm's rules for old packages, so uppercase is allowed; PEP 508, crates.io, RubyGems, Maven
coordinates, Go module paths), checked when the contract is read and
again before the lookup, so a path, a file or a URL never passes for a
name (`left-pad/`, `./x`, `x.tgz`): `not a valid npm package name`. An
image tag and a git branch are resolved to their current
digest or commit.

## Pin or resolve?

| Pin (digest, lockfile, commit) | Resolve (`scope.resolve`) |
|---|---|
| Works offline; the fingerprint needs no network. | Needs the registry every time Aval fingerprints. |
| You update the pin by hand or with a bot. | You follow a tag or a branch on purpose; a range follows every release. |
| What you run is what you hashed. | The id is read when Aval fingerprints; a push between that and your test's own download is not seen. |

Prefer pinning. Use `scope.resolve` for references you mean to follow:
a nightly base image, a tool at `latest`, a fixtures branch.

## How it works

- Each entry is `kind: reference`. The kinds: [`image`](images.md),
  [`npm`](npm.md), [`pypi`](pypi.md), [`go`](go.md), [`maven`](maven.md),
  [`crate`](crates.md), [`gem`](gems.md), [`git`](git.md), [`url`](urls.md).
- The manifest gets one line per reference, after the `env` lines:
  `resolve <kind> <reference> <version> <id>`, where `<version>` is
  `floating` for a reference that names no exact version. `aval run
  --explain` shows them, and prints on stderr how long each lookup took.
- The list is part of the contract hash; its order and duplicates do not
  matter. Contracts without `scope.resolve` never touch the network and keep
  their fingerprint.
- `scope.resolve` binds the artifact you name, not its dependencies. npm,
  pip, `cargo install` without `--locked` and `gem install` still pick
  transitive versions when they install. Pin those with a lockfile.
- No cache: every fingerprint asks the registry again.

## scope.resolve and the scanner

`scope.resolve` never clears a finding of the
[mutable-reference scanner](../pinning.md). A resolved reference makes a
move change the fingerprint: a repushed tag, a moved branch, new bytes at a
URL or a new release all give a miss. But the scanner still reports the
reference, and it still blocks reuse.

To clear the finding, pin it in place: a digest, a commit, a lockfile, a
vendored file. Or accept it on purpose with
[`scope.allowMutable`](../pinning.md#accept-a-reference-on-purpose) and the
reason. `scope.resolve` and `scope.allowMutable` work well together: the
first catches a move, the second says why the reference may stay mutable.

The resolved id vouches for the registry, not for the machine that runs
the test. An engine or a node can run a copy of a tag it pulled earlier:
Compose without `pull_policy: always`, Kubernetes with `IfNotPresent`. Pull
before the test, or pin by digest.

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

`$NETRC` and `$npm_config_userconfig` (any letter case, as npm reads
it) point to other files. Only a
`machine` entry of `~/.netrc` is used, never `default`, and credentials are
sent only over `https` or to a loopback host (also after a redirect).
Credentials go in headers, never in the manifest, a URL or an error. A user, a password or a path
in a registry variable (`GOPROXY=https://user:token@host`,
`https://npm-proxy.fury.io/<token>/user/`) is masked in errors (every URL
in an error shows only its host), and refused over plain `http` to a host that is not loopback. Credentials written in a reference (a password, a user name
in an `http(s)` or scheme-less `//user@host` URL, or a query parameter
such as `token`, `private_token`, `sig`, `client_secret` or
`X-Amz-Signature`, in any case and with `-` or `_`) are
rejected without echoing them; an `ssh://git@host` user is allowed. The registry address is not hashed: the same
content on a mirror resolves to the same id. Requests carry the
User-Agent `aval/<version> (+https://github.com/TrianaLab/aval)`.

The resolved id vouches for the registry in the table, not for the one the
tool installs from. Where the tool's own setting is known to point
elsewhere, resolution fails: an npm `@scope:registry` or `registry` line in
`~/.npmrc`, an `npm_config_@scope:registry` variable,
two spellings of an `npm_config_*` variable (npm reads any letter case, with `_` equal to `-` after the first character) that differ, `$GOPRIVATE`, `$GONOPROXY` or a `$GOPROXY` that starts with
`direct` or `off`, a Go module the checksum database would not verify
(`GOSUMDB=off`, `$GONOSUMDB`, `$GOINSECURE`, `-insecure` in `$GOFLAGS`),
`$PIP_INDEX_URL`, `$PIP_PYPI_URL` (pip's alias of `--index-url`), `$PIP_EXTRA_INDEX_URL`, `$PIP_FIND_LINKS` or
`$PIP_NO_INDEX` (in any spelling pip reads, such as `PIP_index-url`), and a
Maven `<latest>` or `<release>` that is a SNAPSHOT or not listed. A
`~/.npmrc` line Aval cannot read fails too. Other tool configuration files are not read:
`pip.conf`, a project `.npmrc`, `go env -w` settings, Cargo's
`config.toml`, Maven's `settings.xml`, `.gemrc`. If one of them points the
tool to another registry, set the Aval variable to the same registry.

## Read next

- [Pin your dependencies](../pinning.md)
- [The test contract](../contract.md#what-goes-into-the-fingerprint)
