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

  Measured ON THE REAL CI PAYLOAD (run 35021526816, 2026-09-15): **16,498 files,
  188 PE binaries, 78 already vendor-signed, 110 needing ours.**

  ⚠️ **AN EARLIER LOCAL MEASUREMENT SAID 15,007 / 118 / 42 AND IS KEPT HERE
  BECAUSE THE GAP IS THE POINT.** It was taken on a locally-staged payload, and
  it undercounted the work by 2.6x — 42 against 110. Anything sized off it
  (signing-service quota, per-signature cost, batch wall-clock) is wrong in the
  direction that bites. Re-measure from a CI run, never from a dev machine: the
  frozen sidecar's file set is what the freeze produces on that runner, not what
  a local build happens to leave behind.

  Most of the DLLs and .pyd files arrive already signed by their own vendors
  inside Python wheels, so the set needing our signature is still far smaller
  than the file count suggests. This is nothing like the macOS notarization
  problem, where every one of ~16,000 files is scanned.

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
  # ⚠️ THE CATALOG IS WHAT KEEPS THE VENDORS' SIGNATURES ON THE VENDORS' FILES.
  # Azure/artifact-signing-action can sweep a folder itself (`files-folder` +
  # `files-folder-recurse`), and doing that would re-sign all 188 PE binaries —
  # replacing 78 third-party attestations with ours, which is both rude and a
  # loss of information we cannot recreate. So the enumeration stays here, where
  # the already-signed rule lives, and the action is handed the exact list.
  # Paths are written RELATIVE TO THE CATALOG'S OWN LOCATION, which is what the
  # action's `files-catalog` input specifies.
  [string]$CatalogOut,
  # Re-reads a catalog after the action has run and fails on any file that is
  # still unsigned. Separate from -SignCommand's own post-pass because with the
  # action doing the signing this script never sees the result otherwise.
  [string]$VerifyCatalog,
  # ⚠️ NOT named $all: PowerShell variables are CASE-INSENSITIVE, so a local
  # $all would silently overwrite this switch with whatever it held.
  [switch]$SignEverything,
  # Signing services are rate-limited and per-call latency dominates, so files
  # go in batches rather than one invocation each.
  [int]$BatchSize = 20
)

$ErrorActionPreference = 'Stop'

if (-not (Test-Path $PayloadDir)) { throw "payload directory not found: $PayloadDir" }

# ⚠️ RESOLVE TO AN ABSOLUTE PATH BEFORE MEASURING IT. `Get-ChildItem` returns
# absolute `FullName`s, so trimming `$PayloadDir.Length` characters off one is
# only correct when `$PayloadDir` is itself absolute. CI passes it RELATIVE
# (`installers\windows`, 18 chars), so the listing cut 18 characters off
# `D:\a\keld-signal\keld-signal\...` and printed `eld-signal\installers\...` —
# a path that does not exist, in the one output a human reads to check what is
# about to be signed. Display-only, and exactly the kind of wrong that gets
# believed.
$payloadRoot = (Resolve-Path -LiteralPath $PayloadDir).Path

# ⚠️ VERIFY MODE RUNS FIRST AND RETURNS — it must not re-enumerate. After the
# action has signed, "what was supposed to be signed" is the catalog, not a
# fresh scan: a fresh scan would find every file now Valid (including the ones
# the action signed) and could never report a gap, which is the whole failure
# this guards. A signing tool that exits 0 having silently skipped a file is
# what turns into one refused binary on a customer's machine weeks later.
if ($VerifyCatalog) {
  if (-not (Test-Path $VerifyCatalog)) { throw "catalog not found: $VerifyCatalog" }
  $catRoot = Split-Path -Parent (Resolve-Path -LiteralPath $VerifyCatalog).Path
  $entries = Get-Content -LiteralPath $VerifyCatalog | Where-Object { $_.Trim() -ne '' }
  if ($entries.Count -eq 0) { Write-Host "catalog is empty - nothing was queued for signing."; exit 0 }
  $bad = @()
  foreach ($rel in $entries) {
    $full = Join-Path $catRoot $rel.Trim()
    if (-not (Test-Path -LiteralPath $full)) { $bad += "MISSING : $rel"; continue }
    $st = (Get-AuthenticodeSignature -LiteralPath $full).Status
    if ($st -ne 'Valid') { $bad += "$st : $rel" }
  }
  if ($bad.Count -gt 0) {
    Write-Host "::error::$($bad.Count) of $($entries.Count) catalogued binaries are not validly signed"
    $bad | ForEach-Object { Write-Host "  $_" }
    exit 1
  }
  Write-Host "verified signed: $($entries.Count) binaries"
  exit 0
}

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

# Written BEFORE the nothing-to-do exit, so an empty payload produces an empty
# catalog rather than no file at all — the caller then skips the signing step on
# a count it can read, instead of on a missing path it has to interpret.
if ($CatalogOut) {
  $catRoot = Split-Path -Parent $CatalogOut
  if ($catRoot -and -not (Test-Path $catRoot)) { New-Item -ItemType Directory -Force $catRoot | Out-Null }
  $rel = $needed | ForEach-Object { $_.FullName.Substring($payloadRoot.Length).TrimStart('\') }
  # ⚠️ ASCII, NO BOM. signtool's catalog reader takes the file as plain lines;
  # a UTF-8 BOM rides onto the FIRST path and that one file silently fails to
  # resolve — the first entry is keld-agent.exe, i.e. the one that matters.
  # ⚠️ ABSOLUTE PATH, COMPUTED HERE. .NET file APIs resolve a relative path
  # against the PROCESS working directory, which `Set-Location` does not move —
  # so a relative $CatalogOut writes somewhere nobody looks and the step still
  # reports success. Measured once already in this repo's PowerShell.
  $catFull = if ([System.IO.Path]::IsPathRooted($CatalogOut)) { $CatalogOut }
             else { Join-Path (Get-Location).Path $CatalogOut }
  [System.IO.File]::WriteAllLines($catFull, [string[]]@($rel), (New-Object System.Text.ASCIIEncoding))
  Write-Host "catalog written    : $catFull ($(@($rel).Count) entries, relative to $payloadRoot)"
  exit 0
}

if ($needed.Count -eq 0) { Write-Host "nothing to do."; exit 0 }

if (-not $SignCommand) {
  # ⚠️ NOT AN ERROR, AND DELIBERATELY SO. Until a certificate exists the build
  # must still produce an installer — an unsigned one that Smart App Control
  # will refuse, which is exactly what ships today. Failing here would take the
  # whole Windows build down over a purchase order.
  Write-Warning "no -SignCommand given: the payload is UNSIGNED."
  Write-Warning "Smart App Control refuses unsigned binaries; see docs/superpowers/plans/2026-09-15-windows-code-signing-procurement.md"
  $needed | ForEach-Object { Write-Host "  would sign: $($_.FullName.Substring($payloadRoot.Length).TrimStart('\'))" }
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
# ⚠️ EXIT EXPLICITLY. `$LASTEXITCODE` is NOT reset by a script that falls off its
# end — measured: after `cmd /c "exit 7"` a script with no `exit` leaves it at 7 —
# and the caller in installers.yml reads it as this script's verdict. Every other
# path here exits on purpose; without this one the success path would inherit
# whatever the last native command in the caller's shell happened to return.
exit 0
