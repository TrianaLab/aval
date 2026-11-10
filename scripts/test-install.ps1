# Tests scripts/install.ps1 against Aval's real GitHub releases (Windows).
#
#   scripts/test-install.ps1 -Shell powershell   # Windows PowerShell 5.1
#   scripts/test-install.ps1 -Shell pwsh         # PowerShell 7
#
# Each case runs the installer as `irm | iex` does (script text into
# Invoke-Expression) in a fresh $Shell process. Needs network, cosign, gh and
# python. The tamper cases serve a local mirror of a real release through
# AVAL_DOWNLOAD_URL with one file altered, and require the install to fail
# for the expected reason and install nothing. The full tamper matrix runs
# against the bash installer (scripts/test-get-aval.sh); these cases cover
# each check once.
param(
  [string]$Shell = 'pwsh',
  [string]$Version = 'v0.2.0',
  [string]$OldVersion = 'v0.1.0',
  [int]$Port = 8765
)
$ErrorActionPreference = 'Stop'
$ProgressPreference = 'SilentlyContinue'

$installer = Join-Path $PSScriptRoot 'install.ps1'
$work = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $work | Out-Null
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$archive = "aval_$($Version.Substring(1))_windows_$arch.zip"

# Runs the installer with the given environment in a fresh process; returns
# the exit code and leaves the output in $work\<name>.log. The process
# environment (Path included) and the user PATH are restored afterwards, so
# the test does not leave entries behind.
function Invoke-Installer([string]$name, [hashtable]$envs) {
  $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
  $shellExe = (Get-Command $Shell).Source
  $envs['AVAL_INSTALL_DIR'] = Join-Path $work $name
  $saved = @{}
  foreach ($k in $envs.Keys) {
    $saved[$k] = [Environment]::GetEnvironmentVariable($k)
    Set-Item "env:$k" $envs[$k]
  }
  try {
    $ErrorActionPreference = 'Continue'
    & $shellExe -NoProfile -ExecutionPolicy Bypass -Command "Get-Content -Raw '$installer' | Invoke-Expression" *> (Join-Path $work "$name.log")
    return $LASTEXITCODE
  } finally {
    foreach ($k in $envs.Keys) {
      if ($null -eq $saved[$k]) { Remove-Item "env:$k" -ErrorAction SilentlyContinue } else { Set-Item "env:$k" $saved[$k] }
    }
    [Environment]::SetEnvironmentVariable('Path', $userPath, 'User')
  }
}

function Assert-Installed([string]$name, [hashtable]$envs, [string[]]$want) {
  $code = Invoke-Installer $name $envs
  $log = Get-Content -Raw (Join-Path $work "$name.log")
  if ($code -ne 0) { throw "${name}: install failed ($code):`n$log" }
  # Windows PowerShell 5.1 wraps error records at console width, so match without whitespace.
  foreach ($w in $want) { if (-not ($log -replace '\s', '').Contains(($w -replace '\s', ''))) { throw "${name}: output lacks '$w':`n$log" } }
  Write-Host "ok   $name"
}

function Assert-Refused([string]$name, [hashtable]$envs, [string]$want) {
  $code = Invoke-Installer $name $envs
  $log = Get-Content -Raw (Join-Path $work "$name.log")
  if ($code -eq 0) { throw "${name}: install succeeded:`n$log" }
  if (-not ($log -replace '\s', '').Contains(($want -replace '\s', ''))) { throw "${name}: output lacks '$want':`n$log" }
  if (Test-Path (Join-Path $work "$name\aval.exe")) { throw "${name}: aval.exe was installed" }
  Write-Host "ok   $name"
}

$server = $null
try {
  Assert-Installed 'latest' @{} @('signature ok', 'sha256 ok', 'provenance ok', 'aval version ')
  Assert-Installed 'pinned' @{ DESIRED_VERSION = $OldVersion.Substring(1) } @("aval version $($OldVersion.Substring(1))")
  Assert-Refused 'bad-version' @{ DESIRED_VERSION = '1.0;rm' } 'not a release version'
  # Usage errors, refused before anything is downloaded.
  Assert-Refused 'require-verify-no-verify' @{ DESIRED_VERSION = $OldVersion; AVAL_REQUIRE_VERIFY = '1'; AVAL_NO_VERIFY = '1' } 'exclude AVAL_NO_VERIFY'
  Assert-Refused 'require-verify-latest' @{ AVAL_REQUIRE_VERIFY = '1' } 'need DESIRED_VERSION'
  Assert-Refused 'require-provenance-latest' @{ AVAL_REQUIRE_PROVENANCE = '1' } 'need DESIRED_VERSION'
  Assert-Refused 'bad-sha256' @{ AVAL_SHA256 = 'abc' } 'AVAL_SHA256 wants'

  # Local mirror: <case>\<tag>\<file>.
  $real = Join-Path $work 'real'
  New-Item -ItemType Directory -Path $real | Out-Null
  foreach ($f in @($archive, 'checksums.txt', 'checksums.txt.sigstore.json', 'provenance.sigstore.json')) {
    Invoke-WebRequest -UseBasicParsing -Uri "https://github.com/TrianaLab/aval/releases/download/$Version/$f" -OutFile (Join-Path $real $f)
  }
  function New-Mirror([string]$case) {
    $d = Join-Path $work "mirror\$case\$Version"
    New-Item -ItemType Directory -Force -Path $d | Out-Null
    Copy-Item (Join-Path $real '*') $d
    return $d
  }
  $d = New-Mirror 'tampered-archive'
  [IO.File]::AppendAllText((Join-Path $d $archive), 'x')
  $tamperedSha = (Get-FileHash -Algorithm SHA256 (Join-Path $d $archive)).Hash.ToLower()
  $d2 = New-Mirror 'tampered-checksums'
  Copy-Item -Force (Join-Path $d $archive) (Join-Path $d2 $archive)
  $lines = foreach ($l in Get-Content (Join-Path $real 'checksums.txt')) {
    $f = $l -split '\s+'
    if ($f[1] -eq $archive) { "$tamperedSha  $archive" } else { $l }
  }
  # LF line endings and no BOM, as GoReleaser writes it.
  [IO.File]::WriteAllText((Join-Path $d2 'checksums.txt'), (($lines -join "`n") + "`n"))
  New-Mirror 'genuine' | Out-Null
  $genuineSha = (Get-FileHash -Algorithm SHA256 (Join-Path $real $archive)).Hash.ToLower()
  $otherSha = '0' * 64
  # PATH without any directory that holds cosign.
  $noCosign = (($env:Path -split ';') | Where-Object { $_ -and -not (Test-Path (Join-Path $_ 'cosign.exe')) }) -join ';'

  $server = Start-Process python -ArgumentList @('-m', 'http.server', "$Port", '--bind', '127.0.0.1', '--directory', (Join-Path $work 'mirror')) -PassThru -WindowStyle Hidden
  $url = "http://127.0.0.1:$Port"
  foreach ($i in 1..50) {
    try { Invoke-WebRequest -UseBasicParsing -Uri "$url/tampered-archive/$Version/checksums.txt" | Out-Null; break } catch { Start-Sleep -Milliseconds 200 }
  }

  Assert-Refused 'tampered-archive' @{ DESIRED_VERSION = $Version; AVAL_DOWNLOAD_URL = "$url/tampered-archive" } 'checksum mismatch'
  Assert-Refused 'tampered-archive-no-verify' @{ DESIRED_VERSION = $Version; AVAL_DOWNLOAD_URL = "$url/tampered-archive"; AVAL_NO_VERIFY = '1' } 'checksum mismatch'
  Assert-Refused 'tampered-checksums' @{ DESIRED_VERSION = $Version; AVAL_DOWNLOAD_URL = "$url/tampered-checksums" } 'is not signed by'

  # AVAL_SHA256 and AVAL_REQUIRE_VERIFY.
  $genuine = @{ DESIRED_VERSION = $Version; AVAL_DOWNLOAD_URL = "$url/genuine" }
  Assert-Installed 'pinned-verified' ($genuine + @{ AVAL_SHA256 = "linux_amd64=$otherSha windows_$arch=$genuineSha"; AVAL_REQUIRE_VERIFY = '1'; AVAL_REQUIRE_PROVENANCE = '1' }) @('signature ok', 'sha256 ok (pinned)', 'provenance ok')
  Assert-Refused 'wrong-pin' ($genuine + @{ AVAL_SHA256 = $otherSha }) "pinned $otherSha, got $genuineSha"
  Assert-Refused 'other-platform-pin' ($genuine + @{ AVAL_SHA256 = "linux_amd64=$genuineSha" }) "no AVAL_SHA256 entry for windows_$arch"
  Assert-Refused 'pinned-tampered-archive' @{ DESIRED_VERSION = $Version; AVAL_DOWNLOAD_URL = "$url/tampered-archive"; AVAL_SHA256 = $tamperedSha; AVAL_NO_VERIFY = '1' } 'checksum mismatch'
  Assert-Refused 'require-verify-bad-signature' @{ DESIRED_VERSION = $Version; AVAL_DOWNLOAD_URL = "$url/tampered-checksums"; AVAL_SHA256 = $tamperedSha; AVAL_REQUIRE_VERIFY = '1' } 'is not signed by'
  Assert-Refused 'require-verify-no-cosign' ($genuine + @{ Path = $noCosign; AVAL_SHA256 = $genuineSha; AVAL_REQUIRE_VERIFY = '1' }) 'cosign is not installed'
  Write-Host 'all installer cases passed'
} finally {
  if ($server) { Stop-Process -Id $server.Id -Force -ErrorAction SilentlyContinue }
  Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
# The last installer run was a refused one, and the host takes the last
# native exit code as the script's; every failure above throws instead.
exit 0
