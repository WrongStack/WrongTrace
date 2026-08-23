#!/usr/bin/env pwsh
# WrongTrace dev runner (Windows / PowerShell 7): daemon + Vite HMR in one command.
#
# Usage:  ./dev.ps1 [-Port 4318] [-WatchDir .] [-NoBuild] [-NoUI]
#   -Port      daemon + proxy port (default 4318)
#   -WatchDir  directory the daemon observes (default: repo root)
#   -NoBuild   skip the daemon build (reuse bin\wrongtrace.exe as-is)
#   -NoUI      daemon only — no Vite dev server
#
# What it does:
#   1. builds the daemon (fast incremental; avoids `go run` leaving orphaned
#      children behind on interrupt)
#   2. installs web\node_modules on first use
#   3. starts the daemon and the Vite dev server (HMR at :5173, proxying
#      /api + /api/ws to the daemon via WRONGTRACE_PORT)
#   4. Ctrl+C (or process exit) tears BOTH down, including vite's children
#
# POSIX systems: use dev.sh.

[CmdletBinding()]
param(
    [int]$Port = 4318,
    [string]$WatchDir = $PSScriptRoot,
    [switch]$NoBuild,
    [switch]$NoUI
)

$ErrorActionPreference = 'Stop'
$Root = $PSScriptRoot
$Bin = Join-Path $Root 'bin\wrongtrace.exe'
$Socket = '\\.\pipe\wrongtrace'

foreach ($tool in 'go', 'npm') {
    if (-not (Get-Command $tool -ErrorAction SilentlyContinue)) {
        Write-Error "dev.ps1: $tool not found in PATH"
    }
}

if (-not $NoBuild) {
    Write-Host '==> building daemon' -ForegroundColor Cyan
    New-Item -ItemType Directory -Force (Split-Path $Bin) | Out-Null
    Push-Location $Root
    try { go build -o $Bin ./cmd/wrongtrace }
    finally { Pop-Location }
    if ($LASTEXITCODE -ne 0) { Write-Error 'daemon build failed' }
}

# Clean up any lingering wrongtrace processes before starting
Get-Process -Name wrongtrace -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue

if (-not $NoUI -and -not (Test-Path (Join-Path $Root 'web\node_modules'))) {
    Write-Host '==> installing dashboard dependencies (first run)' -ForegroundColor Cyan
    Push-Location (Join-Path $Root 'web')
    try { npm install }
    finally { Pop-Location }
    if ($LASTEXITCODE -ne 0) { Write-Error 'npm install failed' }
}

$daemon = $null
$vite = $null
$wrongtraceHome = if ($env:WRONGTRACE_HOME) { $env:WRONGTRACE_HOME } else { Join-Path $HOME '.wrongtrace' }
New-Item -ItemType Directory -Force $wrongtraceHome | Out-Null
$daemonLog = Join-Path $wrongtraceHome 'daemon.log'
$daemonOut = Join-Path $wrongtraceHome 'daemon-out.log'

try {
    Write-Host "==> starting daemon on :$Port (multi-project workspace hub)" -ForegroundColor Cyan
    $daemonArgs = @(
        'start',
        "--port=$Port",
        "--socket=$Socket"
    )
    if ($WatchDir -ne '') {
        $daemonArgs += "--watch=$WatchDir"
        $daemonArgs += "--repo=$(Split-Path $WatchDir -Leaf)"
    }
    $daemon = Start-Process -FilePath $Bin -PassThru -WindowStyle Hidden -ArgumentList $daemonArgs -RedirectStandardError $daemonLog -RedirectStandardOutput $daemonOut

    for ($i = 0; $i -lt 50; $i++) {
        if ($daemon.HasExited) {
            $errContent = if (Test-Path $daemonLog) { Get-Content $daemonLog -Raw } else { 'unknown error' }
            Write-Error "daemon failed to start: $errContent"
        }
        try {
            Invoke-WebRequest "http://localhost:$Port/api/health" -UseBasicParsing -TimeoutSec 1 | Out-Null
            break
        } catch {
            if ($i -eq 49) { Write-Error "daemon did not become healthy within 10s on port $Port" }
            Start-Sleep -Milliseconds 200
        }
    }
    Write-Host "    daemon healthy: http://localhost:$Port/api/health"

    if (-not $NoUI) {
        Write-Host '==> starting vite dev server' -ForegroundColor Cyan
        # JobProcess tree-kill: vite spawns children, so run it via cmd /c and
        # kill the tree rather than just the shell.
        Push-Location (Join-Path $Root 'web')
        try {
            $env:WRONGTRACE_PORT = "$Port"
            $vite = Start-Process -FilePath 'cmd.exe' -PassThru -WindowStyle Hidden `
                -ArgumentList '/c', 'npm', 'run', 'dev'
        } finally { Pop-Location }

        # Vite is not instant; give the HMR port a moment before announcing.
        Start-Sleep -Seconds 2
    }

    Write-Host ''
    Write-Host 'WrongTrace dev is up:' -ForegroundColor Green
    if (-not $NoUI) { Write-Host '  dashboard (HMR): http://localhost:5173' }
    Write-Host "  daemon API:      http://localhost:$Port"
    Write-Host '  press Ctrl+C to stop both'
    Write-Host ''

    # Park until interrupted or either child exits on its own.
    while ($true) {
        if ($daemon.HasExited) {
            Write-Warning "daemon process stopped (exit code: $($daemon.ExitCode)); shutting down"
            if (Test-Path $daemonLog) {
                Write-Host "--- Last 30 lines of dev-daemon.log (stderr) ---" -ForegroundColor Yellow
                Get-Content $daemonLog | Select-Object -Last 30 | Write-Host -ForegroundColor Red
            }
            if (Test-Path $daemonOut) {
                Write-Host "--- Last 30 lines of dev-daemon-out.log (stdout) ---" -ForegroundColor Yellow
                Get-Content $daemonOut | Select-Object -Last 30 | Write-Host -ForegroundColor Red
            }
            break
        }
        if ($vite -and $vite.HasExited) {
            Write-Warning "vite dev server stopped (exit code: $($vite.ExitCode)); shutting down"
            break
        }
        Start-Sleep -Seconds 1
    }
} finally {
    Write-Host '==> stopping dev processes' -ForegroundColor Cyan

    # Kill the vite tree first (cmd -> npm -> node) so no orphan lingers;
    # taskkill /T walks the whole process tree, unlike Stop-Process.
    if ($vite) {
        & taskkill /PID $vite.Id /T /F 2>$null | Out-Null
    }
    if ($daemon) {
        Stop-Process -Id $daemon.Id -Force -ErrorAction SilentlyContinue
    }
}
