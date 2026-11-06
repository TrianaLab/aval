# One-line installer for Aval (Windows, PowerShell 5.1 or 7).
#
#   irm https://raw.githubusercontent.com/TrianaLab/aval/main/scripts/install.ps1 | iex
#
# The Windows counterpart of scripts/get-aval.sh, with the same checks; read
# that script's header for why each one exists. `docs/install.ps1` is a
# symlink to this file, so the docs site publishes these same bytes.
#
#   * checksums.txt must exist and list the archive, and the archive must have
#     that sha256, or nothing is installed. Always checked.
#   * cosign on PATH: checksums.txt must be signed by TrianaLab/aval's release
#     workflow at this tag.
#   * gh on PATH: the archive must match the release workflow's SLSA
#     provenance (bundle from the release; no login, no API call).
#   * AVAL_NO_VERIFY=1 skips the cosign and gh checks, with a warning.
#
# `irm | iex` cannot pass arguments, so everything is an environment variable:
#   DESIRED_VERSION    release to install, e.g. v0.2.0 (default: latest)
#   AVAL_INSTALL_DIR   install directory (default: %LOCALAPPDATA%\Programs\aval);
#                      added to the user PATH if missing. No admin rights needed.
#   AVAL_NO_VERIFY     "1" skips the cosign and gh checks (never the sha256)
#   AVAL_DOWNLOAD_URL  release download base, for a mirror (default: GitHub
#                      Releases); the signer identity stays pinned.

# Everything runs inside a script block: under `iex` the script shares the
# caller's session, so top-level variables would leak into it, and `exit`
# would close the user's PowerShell window. Errors are thrown instead.
& {
  $ErrorActionPreference = 'Stop'
  # Invoke-WebRequest's progress bar slows downloads by an order of magnitude
  # on Windows PowerShell 5.1.
  $ProgressPreference = 'SilentlyContinue'
  # Windows PowerShell 5.1 may default to TLS 1.0, which GitHub refuses.
  [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

  # Runs a native tool quietly and returns its exit code. Windows PowerShell
  # 5.1 turns redirected stderr of a native command into errors, which
  # 'Stop' would throw on, so the preference is relaxed around the call.
  function Invoke-Tool([string]$exe, [string[]]$argv) {
    $ErrorActionPreference = 'Continue'
    & $exe @argv *> $null
    return $LASTEXITCODE
  }

  $Repo = 'TrianaLab/aval'
  # The identity the release workflow signs with (.github/workflows/release.yaml).
  $Signer = "https://github.com/$Repo/.github/workflows/release.yaml@refs/tags"
  $Issuer = 'https://token.actions.githubusercontent.com'

  $InstallDir = if ($env:AVAL_INSTALL_DIR) { $env:AVAL_INSTALL_DIR } else { Join-Path $env:LOCALAPPDATA 'Programs\aval' }
  $DownloadUrl = if ($env:AVAL_DOWNLOAD_URL) { $env:AVAL_DOWNLOAD_URL } else { "https://github.com/$Repo/releases/download" }
  $NoVerify = $env:AVAL_NO_VERIFY -eq '1'

  # A 32-bit PowerShell on 64-bit Windows reports x86 in
  # PROCESSOR_ARCHITECTURE and the real one in PROCESSOR_ARCHITEW6432.
  $cpu = if ($env:PROCESSOR_ARCHITEW6432) { $env:PROCESSOR_ARCHITEW6432 } else { $env:PROCESSOR_ARCHITECTURE }
  $Arch = switch ($cpu) {
    'AMD64' { 'amd64' }
    'ARM64' { 'arm64' }
    default { throw "unsupported architecture: $cpu" }
  }

  if ($env:DESIRED_VERSION) {
    $Tag = 'v' + ($env:DESIRED_VERSION -replace '^v', '')
  } else {
    # The latest tag comes from the redirect of /releases/latest, not from
    # api.github.com, which allows 60 anonymous requests per hour per IP.
    $resp = Invoke-WebRequest -Uri "https://github.com/$Repo/releases/latest" -Method Head -UseBasicParsing
    # 5.1 exposes the final URL as ResponseUri, 7 as RequestMessage.RequestUri.
    $final = if ($resp.BaseResponse.ResponseUri) { $resp.BaseResponse.ResponseUri } else { $resp.BaseResponse.RequestMessage.RequestUri }
    $Tag = ([string]$final).Split('/')[-1]
  }
  # The tag goes into URLs and the signer identity: release tags only.
  if ($Tag -notmatch '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$') { throw "not a release version: '$Tag'" }
  $Version = $Tag.Substring(1)
  $Archive = "aval_${Version}_windows_${Arch}.zip"
  $Base = "$DownloadUrl/$Tag"

  Write-Host "Installing aval $Tag (windows/$Arch) into $InstallDir"
  $tmp = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
  New-Item -ItemType Directory -Path $tmp | Out-Null
  try {
    function Get-ReleaseFile($name) {
      try {
        Invoke-WebRequest -Uri "$Base/$name" -OutFile (Join-Path $tmp $name) -UseBasicParsing
      } catch {
        throw "release $Tag not found or has no ${name} at $Base"
      }
    }
    Get-ReleaseFile 'checksums.txt'
    Get-ReleaseFile $Archive

    if ($NoVerify) {
      Write-Warning '--no-verify: the signature and provenance are NOT checked, only the sha256'
    }

    # 1. Who published checksums.txt, before its sha256 is trusted.
    if (-not $NoVerify) {
      if (Get-Command cosign -ErrorAction SilentlyContinue) {
        Get-ReleaseFile 'checksums.txt.sigstore.json'
        $code = Invoke-Tool cosign @('verify-blob', (Join-Path $tmp 'checksums.txt'), '--bundle', (Join-Path $tmp 'checksums.txt.sigstore.json'),
          '--certificate-oidc-issuer', $Issuer, '--certificate-identity', "$Signer/$Tag")
        if ($code -ne 0) { throw "checksums.txt is not signed by $Signer/$Tag (cosign verify-blob failed)" }
        Write-Host '  signature ok (cosign: checksums.txt signed by the release workflow)'
      } else {
        Write-Host '  signature not checked: cosign is not installed'
      }
    }

    # 2. The archive matches checksums.txt, by exact file name (a substring
    # match would let the .sbom.json line answer for the archive).
    $expected = $null
    foreach ($line in Get-Content (Join-Path $tmp 'checksums.txt')) {
      $fields = $line -split '\s+'
      if ($fields.Count -ge 2 -and $fields[1] -eq $Archive) { $expected = $fields[0].ToLower() }
    }
    if (-not $expected) { throw "checksums.txt does not list $Archive" }
    $actual = (Get-FileHash -Algorithm SHA256 (Join-Path $tmp $Archive)).Hash.ToLower()
    if ($expected -ne $actual) { throw "checksum mismatch for ${Archive}: expected $expected, got $actual" }
    Write-Host '  sha256 ok'

    # 3. Who built the archive: the release workflow's SLSA provenance. An old
    # gh without `gh attestation` counts as absent.
    if (-not $NoVerify) {
      $gh = (Get-Command gh -ErrorAction SilentlyContinue) -and ((Invoke-Tool gh @('attestation', 'verify', '--help')) -eq 0)
      if ($gh) {
        Get-ReleaseFile 'provenance.sigstore.json'
        $code = Invoke-Tool gh @('attestation', 'verify', (Join-Path $tmp $Archive), '--bundle', (Join-Path $tmp 'provenance.sigstore.json'),
          '--repo', $Repo, '--cert-identity', "$Signer/$Tag", '--cert-oidc-issuer', $Issuer)
        if ($code -ne 0) { throw "$Archive does not match the provenance of $Signer/$Tag (gh attestation verify failed)" }
        Write-Host '  provenance ok (gh: built by the release workflow)'
      } else {
        Write-Host '  provenance not checked: gh (2.49 or later) is not installed'
      }
    }

    Expand-Archive -Path (Join-Path $tmp $Archive) -DestinationPath (Join-Path $tmp 'x')
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    Copy-Item -Force (Join-Path $tmp 'x\aval.exe') (Join-Path $InstallDir 'aval.exe')
  } finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
  }

  # User PATH (no admin), compared entry by entry so a prefix of another
  # directory does not count as present. New shells see it; this one is
  # updated too so `aval` works right away.
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $entries = @($userPath -split ';' | Where-Object { $_ })
  if ($entries -notcontains $InstallDir) {
    [Environment]::SetEnvironmentVariable('Path', (($entries + $InstallDir) -join ';'), 'User')
    Write-Host "  added $InstallDir to the user PATH (open a new terminal to pick it up)"
  }
  if (($env:Path -split ';') -notcontains $InstallDir) { $env:Path = "$env:Path;$InstallDir" }

  Write-Host "aval installed to $(Join-Path $InstallDir 'aval.exe')"
  & (Join-Path $InstallDir 'aval.exe') --version
}
