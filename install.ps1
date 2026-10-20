<#
.SYNOPSIS
    WrongTrace installer for Windows.

.DESCRIPTION
    Downloads the WrongTrace release binary, verifies its SHA-256 checksum when
    the release publishes one, installs it as wrongtrace.exe, and adds the
    install directory to the user PATH.

        irm https://raw.githubusercontent.com/WrongStack/WrongTrace/master/install.ps1 | iex

    Environment overrides (useful with `irm | iex`, which cannot pass parameters):
      WRONGTRACE_VERSION       release tag to install (default: latest), e.g. v0.3.17
      WRONGTRACE_INSTALL_DIR   target directory (default: %LOCALAPPDATA%\Programs\WrongTrace)
      WRONGTRACE_NO_MODIFY_PATH=1  never touch the user PATH

.EXAMPLE
    .\install.ps1 -Version v0.3.17 -InstallDir C:\tools\wrongtrace

.EXAMPLE
    .\install.ps1 -Uninstall
#>
[CmdletBinding()]
param(
    [string]$Version = $env:WRONGTRACE_VERSION,
    [string]$InstallDir = $env:WRONGTRACE_INSTALL_DIR,
    [switch]$NoModifyPath = ($env:WRONGTRACE_NO_MODIFY_PATH -eq '1'),
    [switch]$Uninstall
)

$ErrorActionPreference = 'Stop'
# Invoke-WebRequest's progress bar slows large downloads dramatically on Windows PowerShell 5.1.
$ProgressPreference = 'SilentlyContinue'
# Windows PowerShell 5.1 may default to TLS 1.0, which GitHub rejects.
[Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

$RepoUrl = 'https://github.com/WrongStack/WrongTrace'

function Write-Info([string]$Message) { Write-Host "==> $Message" -ForegroundColor Cyan }
function Write-Warn([string]$Message) { Write-Host "warning: $Message" -ForegroundColor Yellow }

if (-not $InstallDir) {
    $InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\WrongTrace'
}
$InstallDir = [IO.Path]::GetFullPath($InstallDir)
$Target = Join-Path $InstallDir 'wrongtrace.exe'

function Get-UserPathEntries {
    $raw = [Environment]::GetEnvironmentVariable('Path', 'User')
    if (-not $raw) { return @() }
    return @($raw -split ';' | Where-Object { $_ })
}

function Test-SamePath([string]$A, [string]$B) {
    return $A.TrimEnd('\') -ieq $B.TrimEnd('\')
}

if ($Uninstall) {
    foreach ($f in @($Target, "$Target.old")) {
        if (Test-Path $f) { Remove-Item $f -Force; Write-Info "Removed $f" }
    }
    $entries = Get-UserPathEntries
    $kept = @($entries | Where-Object { -not (Test-SamePath $_ $InstallDir) })
    if ($kept.Count -ne $entries.Count) {
        [Environment]::SetEnvironmentVariable('Path', ($kept -join ';'), 'User')
        Write-Info "Removed $InstallDir from the user PATH"
    }
    if ((Test-Path $InstallDir) -and -not (Get-ChildItem $InstallDir -Force)) {
        Remove-Item $InstallDir -Force
    }
    Write-Info 'Data in %WRONGTRACE_HOME% (default ~\.wrongtrace) was left in place; delete it manually if unwanted.'
    return
}

# --- Platform ----------------------------------------------------------------
$cpu = $env:PROCESSOR_ARCHITECTURE
if ($env:PROCESSOR_ARCHITEW6432) { $cpu = $env:PROCESSOR_ARCHITEW6432 }
switch ($cpu) {
    'AMD64' { $Arch = 'amd64' }
    'ARM64' {
        # No native arm64 build yet; Windows on ARM runs the amd64 binary under emulation.
        Write-Warn 'No native Windows ARM64 build is published; installing the amd64 binary (runs under emulation).'
        $Arch = 'amd64'
    }
    default { throw "Unsupported architecture '$cpu'. Build from source: $RepoUrl" }
}

# --- Version -----------------------------------------------------------------
if (-not $Version) {
    Write-Info 'Resolving latest release'
    $req = [Net.HttpWebRequest]::Create("$RepoUrl/releases/latest")
    $req.Method = 'HEAD'
    $req.AllowAutoRedirect = $true
    $resp = $req.GetResponse()
    try { $final = $resp.ResponseUri.AbsoluteUri } finally { $resp.Close() }
    if ($final -notmatch '/tag/([^/]+)$') {
        throw "Could not determine the latest release (resolved to $final)"
    }
    $Version = $Matches[1]
}
if (-not $Version.StartsWith('v')) { $Version = "v$Version" }

$Asset = "wrongtrace-$Version-windows-$Arch.exe"
$Base = "$RepoUrl/releases/download/$Version"

$Tmp = Join-Path ([IO.Path]::GetTempPath()) ("wrongtrace-install-" + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $Tmp | Out-Null
try {
    $Download = Join-Path $Tmp 'wrongtrace.exe'
    Write-Info "Downloading WrongTrace $Version (windows-$Arch)"
    try {
        Invoke-WebRequest -Uri "$Base/$Asset" -OutFile $Download -UseBasicParsing
    } catch {
        throw "Download failed: $Base/$Asset ($($_.Exception.Message))"
    }

    # --- Checksum ------------------------------------------------------------
    $sumsFile = Join-Path $Tmp 'checksums.txt'
    $haveSums = $true
    try {
        Invoke-WebRequest -Uri "$Base/checksums.txt" -OutFile $sumsFile -UseBasicParsing
    } catch {
        $haveSums = $false
        Write-Warn "Release $Version publishes no checksums.txt; skipping checksum verification."
    }
    if ($haveSums) {
        $expected = $null
        foreach ($line in Get-Content $sumsFile) {
            $parts = $line -split '\s+', 2
            if ($parts.Count -eq 2 -and $parts[1].TrimStart('*').Trim() -eq $Asset) { $expected = $parts[0] }
        }
        if (-not $expected) { throw "$Asset is not listed in checksums.txt" }
        $actual = (Get-FileHash -Algorithm SHA256 $Download).Hash
        if ($actual -ine $expected) { throw "Checksum mismatch (expected $expected, got $actual)" }
        Write-Info 'Checksum verified'
    }

    & $Download --version *> $null
    if ($LASTEXITCODE -ne 0) { throw 'The downloaded binary does not run on this system.' }

    # --- Install -------------------------------------------------------------
    New-Item -ItemType Directory -Force -Path $InstallDir | Out-Null
    if (Test-Path "$Target.old") { Remove-Item "$Target.old" -Force -ErrorAction SilentlyContinue }
    if (Test-Path $Target) {
        # A running daemon locks wrongtrace.exe against overwrite but not rename.
        Move-Item $Target "$Target.old" -Force
    }
    try {
        Move-Item $Download $Target -Force
    } catch {
        if (Test-Path "$Target.old") { Move-Item "$Target.old" $Target -Force }
        throw
    }
    Remove-Item "$Target.old" -Force -ErrorAction SilentlyContinue
    Write-Info "Installed $(& $Target --version) to $Target"
} finally {
    Remove-Item $Tmp -Recurse -Force -ErrorAction SilentlyContinue
}

# --- PATH --------------------------------------------------------------------
$onUserPath = [bool](Get-UserPathEntries | Where-Object { Test-SamePath $_ $InstallDir })
if (-not $onUserPath) {
    if ($NoModifyPath) {
        Write-Warn "$InstallDir is not on your PATH; add it manually."
    } else {
        $entries = @(Get-UserPathEntries) + $InstallDir
        [Environment]::SetEnvironmentVariable('Path', ($entries -join ';'), 'User')
        Write-Info "Added $InstallDir to the user PATH (new terminals pick it up automatically)"
    }
}
# Make it usable in this session immediately.
if (-not (($env:Path -split ';') | Where-Object { $_ -and (Test-SamePath $_ $InstallDir) })) {
    $env:Path = "$InstallDir;$env:Path"
}

Write-Host ''
Write-Info 'Done. Get started with:'
Write-Host '    wrongtrace init     # agent rules, .mcp.json, git hooks'
Write-Host '    wrongtrace start    # daemon + dashboard on http://localhost:3444'
Write-Host '    wrongtrace update   # upgrade to the latest release later'
