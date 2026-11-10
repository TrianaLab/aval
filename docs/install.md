# Install

How do I get `aval`? One static binary for Linux, macOS and Windows (amd64,
arm64) from [GitHub Releases](https://github.com/TrianaLab/aval/releases),
checked before it is installed.

## Linux and macOS

```sh
curl -fsSL https://aval.run/install.sh | bash
```

Installs into `/usr/local/bin` (`sudo` only if needed). Needs `curl` or
`wget`, and `sha256sum` or `shasum`.

| Variable | Flag | Windows | Effect |
|---|---|---|---|
| `DESIRED_VERSION=v0.2.0` | `--version v0.2.0` | yes | install this release, not the latest |
| `AVAL_INSTALL_DIR=<dir>` | | yes | install here |
| `USE_SUDO=false` | `--no-sudo` | no | never use `sudo` |
| `AVAL_NO_VERIFY=1` | `--no-verify` | yes | skip the cosign and gh checks (sha256 still runs) |
| `AVAL_DOWNLOAD_URL=<url>` | | yes | download from a mirror; the signer stays pinned |
| `AVAL_SHA256=<hex>` | `--sha256 <hex>` | yes | the archive must have this sha256; `<os>_<arch>=<hex>` pins one platform (repeat the flag, or separate with spaces) |
| `AVAL_REQUIRE_VERIFY=1` | `--require-verify` | yes | fail if `cosign` is missing; needs a version, excludes `--no-verify` |
| `AVAL_REQUIRE_PROVENANCE=1` | `--require-provenance` | yes | fail if `gh` 2.49 or later is missing; needs a version, excludes `--no-verify` |

Piped, pass flags after `bash -s --`:

```sh
curl -fsSL https://aval.run/install.sh | bash -s -- --version v0.2.0
```

## Windows

In PowerShell 5.1 or 7:

```powershell
irm https://aval.run/install.ps1 | iex
```

Installs into `%LOCALAPPDATA%\Programs\aval` and adds it to your user
`PATH`, no admin rights. Settings: the variables marked Windows above.

## What the installers check

| Check | Runs |
|---|---|
| the archive's sha256 matches `checksums.txt` | always |
| the archive's sha256 is the pinned one | with `--sha256` |
| `checksums.txt` has a Sigstore signature from Aval's release workflow at that tag | if `cosign` is on `PATH`; required with `--require-verify` |
| the archive matches the release's SLSA provenance | if `gh` 2.49 or later is on `PATH` (no login); required with `--require-provenance` |

A failed check installs nothing. A check that cannot run is reported, or is
an error with `--require-verify` / `--require-provenance`.

## In CI

Install one exact release and nothing you cannot verify. Vendor the
installer at a release tag, so a change on `main` cannot alter your pipeline,
then pin the version and the archive's sha256 (from that release's
`checksums.txt`):

```sh
curl -fsSLo .ci/get-aval.sh https://raw.githubusercontent.com/TrianaLab/aval/v0.8.0/scripts/get-aval.sh
```

```sh
# cosign (and gh for --require-provenance) must be on PATH.
bash .ci/get-aval.sh --version v0.8.0 \
  --sha256 linux_amd64=<sha256 of aval_0.8.0_linux_amd64.tar.gz> \
  --sha256 darwin_arm64=<sha256 of aval_0.8.0_darwin_arm64.tar.gz> \
  --require-verify --require-provenance
```

The entry for the runner's platform is required. A re-tagged release, a
mirror serving another build, or a missing `cosign` fails the install.
`--sha256`, `--require-verify` and `--require-provenance` first ship in the
release after 0.7.0. Aval's own CI does the same with `main`'s copy
(`.github/aval/install.sh`).

## Upgrade

```sh
aval upgrade                    # to the latest release
aval upgrade --version v0.4.0   # to a given release
aval upgrade --check            # print the current and target versions, change nothing
```

- It checks the Sigstore signature of `checksums.txt` itself (issuer
  `https://token.actions.githubusercontent.com`, signer: Aval's release
  workflow at that tag), then the sha256. No `cosign` needed.
- A failed check replaces nothing and exits 1. A bad signature is a security
  event: do not retry it away.
- Without `--version` it never downgrades; a `dev` build is always replaced.
- Directory not writable? Use `sudo` or the installer.

`aval version` prints the version, Go version and platform.

## By hand

Download, check, then put `aval` on your `PATH`:

```sh
VERSION=0.2.0
OS=linux        # or darwin
ARCH=amd64      # or arm64
BASE=https://github.com/TrianaLab/aval/releases/download/v$VERSION
curl -fsSLO "$BASE/aval_${VERSION}_${OS}_${ARCH}.tar.gz"
curl -fsSLO "$BASE/checksums.txt"
curl -fsSLO "$BASE/checksums.txt.sigstore.json"
curl -fsSLO "$BASE/provenance.sigstore.json"
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity "https://github.com/TrianaLab/aval/.github/workflows/release.yaml@refs/tags/v$VERSION"
sha256sum --ignore-missing -c checksums.txt   # macOS: shasum -a 256 --ignore-missing -c checksums.txt
gh attestation verify "aval_${VERSION}_${OS}_${ARCH}.tar.gz" --bundle provenance.sigstore.json --repo TrianaLab/aval
tar -xzf "aval_${VERSION}_${OS}_${ARCH}.tar.gz" aval
sudo mv aval /usr/local/bin/
```

## From source

`go install github.com/TrianaLab/aval/cmd/aval@latest`. The binary reports
version `dev` and has no release checks.

## Uninstall

Delete `/usr/local/bin/aval` (or your `AVAL_INSTALL_DIR`), or
`%LOCALAPPDATA%\Programs\aval` on Windows and remove it from your user `PATH`.
