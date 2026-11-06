# Install

Aval is one static binary for Linux, macOS and Windows on amd64 and arm64.
The installers download it from
[GitHub Releases](https://github.com/TrianaLab/aval/releases) and check it
before installing.

## Linux and macOS

```sh
curl -fsSL https://raw.githubusercontent.com/TrianaLab/aval/main/scripts/get-aval.sh | bash
```

The script needs `curl` or `wget` and `sha256sum` or `shasum`, and works with
the bash 3.2 that macOS ships. It installs `aval` into `/usr/local/bin` and
uses `sudo` only when that directory is not writable.

| Variable or flag | Effect |
|---|---|
| `DESIRED_VERSION=v0.2.0`, `--version v0.2.0` | install this release instead of the latest |
| `AVAL_INSTALL_DIR=$HOME/.local/bin` | install into this directory |
| `USE_SUDO=false`, `--no-sudo` | never use `sudo` |
| `AVAL_NO_VERIFY=1`, `--no-verify` | skip the signature and provenance checks (the sha256 is still checked) |
| `AVAL_DOWNLOAD_URL` | download from a mirror of the release files; the signer identity does not change |

Flags need the script as a file, or `bash -s --` when piped:

```sh
curl -fsSL https://raw.githubusercontent.com/TrianaLab/aval/main/scripts/get-aval.sh | bash -s -- --version v0.2.0
```

## Windows

In PowerShell 5.1 or 7:

```powershell
irm https://raw.githubusercontent.com/TrianaLab/aval/main/scripts/install.ps1 | iex
```

It installs `aval.exe` into `%LOCALAPPDATA%\Programs\aval` and adds that
directory to the user `PATH`, without administrator rights. It reads
`DESIRED_VERSION`, `AVAL_INSTALL_DIR`, `AVAL_NO_VERIFY` and
`AVAL_DOWNLOAD_URL` from the environment.

## What the installers check

Both scripts stop without installing anything when a check fails:

1. `checksums.txt` must exist in the release and list the archive, and the
   archive must have that sha256. This check always runs.
2. If `cosign` is on `PATH`, `checksums.txt` must carry a keyless Sigstore
   signature from `https://github.com/TrianaLab/aval/.github/workflows/release.yaml@refs/tags/<tag>`.
3. If `gh` 2.49 or later is on `PATH`, the archive must match the SLSA
   provenance from the same workflow (`gh attestation verify` with the
   `provenance.sigstore.json` bundle of the release; no GitHub login needed).

Without `cosign` or `gh`, the installer prints which check did not run. To
check a downloaded release by hand:

```sh
cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  --certificate-identity https://github.com/TrianaLab/aval/.github/workflows/release.yaml@refs/tags/v0.2.0
sha256sum --ignore-missing -c checksums.txt
gh attestation verify aval_0.2.0_linux_amd64.tar.gz --bundle provenance.sigstore.json --repo TrianaLab/aval
```

## Go users

`go install github.com/TrianaLab/aval/cmd/aval@latest` builds from source
instead. The binary reports version `dev` and skips the release checks above.

## Uninstall

Delete the binary: `/usr/local/bin/aval` (or your `AVAL_INSTALL_DIR`), or
`%LOCALAPPDATA%\Programs\aval` on Windows, and remove that directory from the
user `PATH`.
