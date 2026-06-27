# keld installer for Windows (PowerShell)
# Usage: irm https://raw.githubusercontent.com/ncx-ai/keld-cli/main/scripts/install.ps1 | iex
#Requires -Version 5.1
Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$REPO = 'ncx-ai/keld-cli'
$InstallDir = if ($env:KELD_INSTALL_DIR) { $env:KELD_INSTALL_DIR } `
              else { Join-Path $env:LOCALAPPDATA 'Programs\keld' }

# ── Arch detection ────────────────────────────────────────────────────────────
$arch = $env:PROCESSOR_ARCHITECTURE
# Normalize: AMD64 is the only published Windows target.
if ($arch -ne 'AMD64') {
    Write-Error "keld installer: unsupported architecture: $arch.`n  Only AMD64 (x86-64) is currently supported on Windows."
    exit 1
}

# ── Latest release tag ────────────────────────────────────────────────────────
$apiUrl = "https://api.github.com/repos/$REPO/releases/latest"
try {
    $release = Invoke-RestMethod -Uri $apiUrl -UseBasicParsing
} catch {
    Write-Error "keld installer: could not reach GitHub API.`n  Check your network connection or visit: https://github.com/$REPO/releases/latest`n  Error: $_"
    exit 1
}
$tag = $release.tag_name
if (-not $tag) {
    Write-Error "keld installer: could not determine the latest release tag."
    exit 1
}

# ── Download and extract ──────────────────────────────────────────────────────
$archive  = "keld_windows_amd64.zip"
$url      = "https://github.com/$REPO/releases/download/$tag/$archive"
$tmpZip   = Join-Path $env:TEMP "keld_windows_amd64.zip"

Write-Host "Installing keld $tag (windows/amd64)..."
Write-Host "  Source:      $url"
Write-Host "  Destination: $InstallDir\keld.exe"

try {
    Invoke-WebRequest -Uri $url -OutFile $tmpZip -UseBasicParsing
} catch {
    Write-Error "keld installer: download failed.`n  URL: $url`n  Make sure the release exists and your network can reach github.com.`n  Error: $_"
    exit 1
}

if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir -Force | Out-Null
}

try {
    Expand-Archive -Path $tmpZip -DestinationPath $InstallDir -Force
} catch {
    Write-Error "keld installer: extraction failed.`n  Error: $_"
    exit 1
} finally {
    Remove-Item $tmpZip -ErrorAction SilentlyContinue
}

Write-Host ""
Write-Host "keld $tag installed to $InstallDir\keld.exe"
Write-Host ""
Write-Host "Next steps:"
Write-Host "  1. Add $InstallDir to your PATH (if not already)."
Write-Host "     Run this once in an elevated PowerShell to add it permanently:"
Write-Host "       [Environment]::SetEnvironmentVariable('PATH', `$env:PATH + ';$InstallDir', 'User')"
Write-Host "  2. Open a new terminal, then run:  keld login"
Write-Host "  3. Run:  keld signal setup"
Write-Host ""
Write-Host "Note: Windows SmartScreen may warn on first run — unsigned binaries"
Write-Host "  trigger this. Click 'More info' > 'Run anyway' to proceed."
Write-Host "  Code signing is a planned follow-up."
