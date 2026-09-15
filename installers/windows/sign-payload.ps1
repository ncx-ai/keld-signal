<#
.SYNOPSIS
  Authenticode-sign every binary in the Windows payload that needs it.

.DESCRIPTION
  ⚠️ **SIGNING THE INSTALLER ALONE DOES NOT WORK, AND THAT IS THE WHOLE REASON
  THIS SCRIPT EXISTS.** Smart App Control evaluates binaries AS THEY LOAD, not
  once at install time — Microsoft's own developer guidance says to test "all of
  your app's install and uninstall binaries". So a signed keld-setup.exe that
  installs an unsigned keld.exe buys nothing: the installer runs, and the product
  is refused the moment it starts.

  Measured on the payload (2026-09-15): 15,007 files, of which only **118** are
  PE binaries — 5 .exe, 65 .dll, 48 .pyd. Most of the DLLs and .pyd files arrive
  already signed by their own vendors inside Python wheels, so the set that
  actually needs our signature is far smaller than the file count suggests. This
  is nothing like the macOS notarization problem, where every one of ~15,000
  files is scanned.

  ⚠️ **AN ALREADY-VALID THIRD-PARTY SIGNATURE IS LEFT ALONE.** Re-signing a
  vendor's DLL replaces their attestation with ours, which is both rude and a
  loss of information — their signature says something we cannot say about code
  we did not build. `-SignEverything` overrides this for the rare case where a vendor's
  signature is itself untrusted.

.PARAMETER PayloadDir
  Directory to sign, recursively — the staging directory handed to iscc.

.PARAMETER SignCommand
  Command template invoked once per batch, with {FILES} replaced by the
  space-separated, quoted file list. Vendor-neutral on purpose: the certificate
  authority is not chosen yet, and every cloud signing service exposes a
  different tool (signtool with a CSP/KSP, SSL.com's CodeSignTool, DigiCert's
  smctl). Whichever it is, it goes here and nothing else in the build changes.

.PARAMETER SignEverything
  Sign every PE binary, including ones that already carry a valid signature.

.EXAMPLE
  ./sign-payload.ps1 -PayloadDir installers\windows -SignCommand 'signtool sign /fd SHA256 /tr http://timestamp.digicert.com /td SHA256 /csp "DigiCert Signing Manager KSP" /kc $env:SM_KEYPAIR_ALIAS /f cert.pem {FILES}'

.EXAMPLE
  ./sign-payload.ps1 -PayloadDir installers\windows -WhatIf
  Lists what would be signed and exits — safe with no certificate at all.
#>
[CmdletBinding(SupportsShouldProcess)]
param(
  [Parameter(Mandatory)][string]$PayloadDir,
  [string]$SignCommand,
  # ⚠️ NOT named $all: PowerShell variables are CASE-INSENSITIVE, so a local
  # $all would silently overwrite this switch with whatever it held.
  [switch]$SignEverything,
  # Signing services are rate-limited and per-call latency dominates, so files
  # go in batches rather than one invocation each.
  [int]$BatchSize = 20
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path $PayloadDir)) { throw "payload directory not found: $PayloadDir" }

# The extensions Windows loads as code. Data files are not evaluated and must not
# be signed — signing them wastes quota and tells the reader something false
# about what the build does.
$codeExtensions = @('.exe', '.dll', '.pyd', '.sys', '.ocx')

$binaries = Get-ChildItem -Path $PayloadDir -Recurse -File |
       Where-Object { $codeExtensions -contains $_.Extension.ToLower() }

if ($binaries.Count -eq 0) { throw "no PE binaries found under $PayloadDir — wrong directory, or the payload was never staged" }

$needed = @()
$alreadySigned = @()
foreach ($f in $binaries) {
  $sig = Get-AuthenticodeSignature -LiteralPath $f.FullName
  if ((-not $SignEverything) -and $sig.Status -eq 'Valid') { $alreadySigned += $f } else { $needed += $f }
}

Write-Host "payload            : $PayloadDir"
Write-Host "files total        : $((Get-ChildItem $PayloadDir -Recurse -File).Count)"
Write-Host "PE binaries        : $($binaries.Count)"
Write-Host "already signed     : $($alreadySigned.Count) (left untouched)"
Write-Host "to sign            : $($needed.Count)"

if ($needed.Count -eq 0) { Write-Host "nothing to do."; exit 0 }

if (-not $SignCommand) {
  # ⚠️ NOT AN ERROR, AND DELIBERATELY SO. Until a certificate exists the build
  # must still produce an installer — an unsigned one that Smart App Control
  # will refuse, which is exactly what ships today. Failing here would take the
  # whole Windows build down over a purchase order.
  Write-Warning "no -SignCommand given: the payload is UNSIGNED."
  Write-Warning "Smart App Control refuses unsigned binaries; see docs/superpowers/plans/2026-09-15-windows-code-signing-procurement.md"
  $needed | ForEach-Object { Write-Host "  would sign: $($_.FullName.Substring($PayloadDir.Length).TrimStart('\'))" }
  exit 0
}

$failed = @()
for ($i = 0; $i -lt $needed.Count; $i += $BatchSize) {
  $batch = $needed[$i..([Math]::Min($i + $BatchSize - 1, $needed.Count - 1))]
  $quoted = ($batch | ForEach-Object { '"{0}"' -f $_.FullName }) -join ' '
  $cmd = $SignCommand.Replace('{FILES}', $quoted)
  if ($PSCmdlet.ShouldProcess("$($batch.Count) files", "sign")) {
    Invoke-Expression $cmd
    if ($LASTEXITCODE -ne 0) { $failed += $batch }
  }
}

if ($WhatIfPreference) { exit 0 }

# ⚠️ VERIFY AFTERWARDS, AND FAIL THE BUILD ON A GAP. A signing tool that exits 0
# having silently skipped a file is the failure this whole exercise is about:
# the build looks clean, the installer ships, and one binary is refused on a
# customer's machine weeks later with nothing to point at.
$unsigned = @()
foreach ($f in $needed) {
  if ((Get-AuthenticodeSignature -LiteralPath $f.FullName).Status -ne 'Valid') { $unsigned += $f }
}
if ($unsigned.Count -gt 0) {
  Write-Host "::error::$($unsigned.Count) binaries are still unsigned after signing"
  $unsigned | ForEach-Object { Write-Host "  UNSIGNED: $($_.FullName)" }
  exit 1
}

Write-Host "signed and verified: $($needed.Count) binaries"
