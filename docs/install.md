# Install

One static binary for Linux, macOS and Windows (amd64, arm64), from
[GitHub Releases](https://github.com/TrianaLab/aval/releases). The installers
check the download before they install it.

## Linux and macOS

```sh
curl -fsSL https://aval.run/install.sh | bash
```

Installs into `/usr/local/bin`, with `sudo` only if that directory is not
writable. Needs `curl` or `wget`, and `sha256sum` or `shasum`. Works with the
bash 3.2 that macOS ships.

| Variable | Flag | Windows | Effect |
|---|---|---|---|
| `DESIRED_VERSION=v0.2.0` | `--version v0.2.0` | yes | install this release, not the latest |
| `AVAL_INSTALL_DIR=<dir>` | | yes | install here |
| `USE_SUDO=false` | `--no-sudo` | no | never use `sudo` |
| `AVAL_NO_VERIFY=1` | `--no-verify` | yes | skip the cosign and gh checks (sha256 still runs) |
| `AVAL_DOWNLOAD_URL=<url>` | | yes | download from a mirror; the signer stays pinned |

Piped, pass flags after `bash -s --`:

```sh
curl -fsSL https://aval.run/install.sh | bash -s -- --version v0.2.0
```

## Windows

In PowerShell 5.1 or 7:

```powershell
irm https://aval.run/install.ps1 | iex
```

Installs `aval.exe` into `%LOCALAPPDATA%\Programs\aval` and adds it to your
user `PATH`, without admin rights. Settings: the variables marked Windows
above.

## What the installers check

| Check | Runs |
|---|---|
| the archive's sha256 matches `checksums.txt` | always |
| `checksums.txt` has a Sigstore signature from Aval's release workflow at that tag | if `cosign` is on `PATH` |
| the archive matches the release's SLSA provenance | if `gh` 2.49 or later is on `PATH` (no login) |

A failed check installs nothing. A check that cannot run is reported.

## Upgrade

```sh
aval upgrade                    # to the latest release
aval upgrade --version v0.4.0   # to a given release
aval upgrade --check            # print the current and target versions, change nothing
```

`aval upgrade` replaces the running binary. It needs no `cosign`: it checks
the Sigstore signature of `checksums.txt` itself (issuer
`https://token.actions.githubusercontent.com`, signer Aval's release workflow
at that tag), then the archive's sha256. A failed check replaces nothing and
exits 1; a bad signature or checksum is a security event, do not retry it
away. Without `--version` it refuses to move to a release older than the
running one; a `dev` build is always replaced. If the binary's directory is
not writable, run it with `sudo` or use the installer again. `aval version` prints the version, Go version and platform.

## By hand

Download the archive, `checksums.txt` and the signatures, check them, then put
`aval` on your `PATH`:

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
