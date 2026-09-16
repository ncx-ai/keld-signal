<#
.SYNOPSIS
  Install Signal on Windows from the REAL installer, unattended, and prove it (AC-12).

.DESCRIPTION
  ⚠️ **UNVERIFIED — THIS SCRIPT HAS NEVER BEEN RUN.** It was written on a macOS
  machine with no PowerShell and no Windows runner, so not even its PARSE has
  been checked; `bash -n`'s equivalent was unavailable. Treat the first CI run
  as the test and fix this paragraph when it passes, exactly as
  scripts/conformance/tart/up.sh does. The reason it exists anyway is AC-12:
  the criterion is about the installer having an unattended mode, and the
  invocation below is the one an MDM push uses. What is unproven is this
  wrapper, not `/VERYSILENT`.

  Three phases:

    1. INSTALL, unattended:  keld-setup.exe /VERYSILENT /SUPPRESSMSGBOXES /NORESTART
    2. ONBOARD, by command:  keld login --code · keld signal setup --yes · keld-agent install
    3. VERIFY, from OBSERVED STATE: the binaries run, Task Scheduler holds the
       KeldAgent task, and hook.json carries an ingest token. Never from an exit
       code.

  ⚠️ **WHAT /VERYSILENT DOES TO THE INSTALLER'S OWN ONBOARDING, AND WHY THAT IS
  CORRECT.** installers/windows/keld-agent.iss has two [Run] entries. The first,
  `keld-agent.exe install --headless`, ALWAYS runs — it writes the v2 config,
  registers the logon task and prompts for nothing, and it exists precisely
  because putting registration behind the postinstall checkbox meant a /SILENT
  push installed the files and registered NOTHING. The second, `onboard.cmd`,
  carries `skipifsilent`, so a silent push never opens a console at a human who
  is not there. The machine is left registered and unpaired — the documented
  `awaitConfig` idle state — and phase 2 is what finishes it. That is the MDM
  path exactly: push, then `keld-agent install --code <CODE>`.

  ⚠️ /VERYSILENT rather than /SILENT: /SILENT still shows the progress window,
  and the payload is the frozen sidecar's ~15,000 files, so that window lives
  for minutes on a machine nobody is watching.

  ⚠️ This rewrites the machine it runs on. It refuses unless $env:CI is true or
  KELD_CONFORM_DISPOSABLE=1 — the same guard the POSIX scripts use, for the same
  reason: registration and `keld signal setup` are machine-wide, not isolated by
  KELD_HOME.

.EXAMPLE
  pwsh scripts/conformance/install-windows.ps1 -Setup artifacts\keld-setup.exe -Code CONFORM
  pwsh scripts/conformance/install-windows.ps1 -Artifacts artifacts -Code CONFORM -ApiUrl http://127.0.0.1:8123
#>
[CmdletBinding()]
param(
  [string]$Setup = $env:KELD_CONFORM_SETUP_EXE,
  [string]$Artifacts = "",
  [string]$Code = $env:KELD_SETUP_CODE,
  [string]$ApiUrl = $env:KELD_API_URL,
  [switch]$SkipOnboarding,
  [switch]$StopService
)

$ErrorActionPreference = "Continue"
$script:Step = "start"
$Name = "install-windows"

function Set-Step([string]$s) { $script:Step = $s; Write-Host "${Name}: --- step $s ---" }
function Say([string]$m)      { Write-Host "${Name}: $m" }
function Ok([string]$m)       { Write-Host "${Name}:   OK $m" }
function Die([string]$m) {
  Write-Host ""
  Write-Error "${Name}: FAILED at step $($script:Step): $m"
  Write-Host "${Name}: the machine is NOT installed and onboarded; nothing downstream may assume it is."
  exit 1
}

# --- the guard ---------------------------------------------------------------
if (-not ($env:CI -eq "true" -or $env:KELD_CONFORM_DISPOSABLE -eq "1")) {
  Write-Host @"

REFUSING TO RUN - this machine has not been declared disposable.

  This script installs Signal for real: it registers the KeldAgent scheduled
  task and lets "keld signal setup" rewrite your AI tools' own config files.
  KELD_HOME does not isolate either of those.

  Run it in a throwaway VM or in CI. To override deliberately, set:

      KELD_CONFORM_DISPOSABLE=1

"@
  exit 2
}

# --- 1. install, unattended ---------------------------------------------------

Set-Step "resolve-setup-exe"
if (-not $Setup -and $Artifacts) {
  # Newest first: an artifacts dir from two runs holds two installers, and
  # silently testing the older one is worse than refusing.
  $Setup = (Get-ChildItem -Path $Artifacts -Filter "keld-setup*.exe" -Recurse -ErrorAction SilentlyContinue |
            Sort-Object LastWriteTime -Descending | Select-Object -First 1).FullName
}
if (-not $Setup)               { Die "no installer given (-Setup, -Artifacts or `$env:KELD_CONFORM_SETUP_EXE)" }
if (-not (Test-Path $Setup))   { Die "no such file: $Setup" }
$Setup = (Resolve-Path $Setup).Path
Ok "$Setup ($([math]::Round((Get-Item $Setup).Length / 1MB)) MB)"

Set-Step "inno-verysilent"
$log = Join-Path ([System.IO.Path]::GetTempPath()) "keld-setup-install.log"
# /LOG is not decoration: Inno's own log is the only account of a failed [Run]
# entry on a machine with no console, and this is the class of failure that went
# unnoticed for a whole Windows release.
$innoArgs = @("/VERYSILENT", "/SUPPRESSMSGBOXES", "/NORESTART", "/LOG=$log")
Say "`$ $Setup $($innoArgs -join ' ')"
$p = Start-Process -FilePath $Setup -ArgumentList $innoArgs -Wait -PassThru
if ($p.ExitCode -ne 0) {
  if (Test-Path $log) { Get-Content $log -Tail 30 | ForEach-Object { Write-Host "${Name}:   | $_" } }
  Die "the installer exited $($p.ExitCode) (Inno log: $log)"
}
Ok "installer exited 0; Inno log at $log"

$App = Join-Path $env:LOCALAPPDATA "Programs\keld"
$Keld = Join-Path $App "keld.exe"
$Agent = Join-Path $App "keld-agent.exe"

# --- 2. onboard, by command ---------------------------------------------------

if ($SkipOnboarding) {
  Say "onboarding skipped (-SkipOnboarding): only the install and the task are verified"
} else {
  if (-not $Code) { Die "no setup code (-Code or `$env:KELD_SETUP_CODE); onboarding cannot run unattended without one" }
  $api = @()
  if ($ApiUrl) { $api = @("--api-url", $ApiUrl) }

  Set-Step "keld-login"
  Say "`$ keld.exe login --code <redacted> $($api -join ' ')"
  & $Keld login --code $Code @api 2>&1 | ForEach-Object { Write-Host "${Name}:   | $_" }
  if ($LASTEXITCODE -ne 0) { Die "keld login --code was rejected (is Atlas reachable at $ApiUrl?)" }

  Set-Step "keld-signal-setup"
  & $Keld signal setup --yes @api 2>&1 | ForEach-Object { Write-Host "${Name}:   | $_" }
  if ($LASTEXITCODE -ne 0) { Die "keld signal setup --yes failed" }

  # Idempotent: the .iss [Run] entry already ran `install --headless`. Running it
  # again is what an MDM push does after handing the machine a code, and it
  # restarts the task so the new config is read - ml_backend is read at daemon
  # STARTUP and never re-read.
  Set-Step "keld-agent-install"
  & $Agent install 2>&1 | ForEach-Object { Write-Host "${Name}:   | $_" }
  if ($LASTEXITCODE -ne 0) { Die "keld-agent install failed" }
}

# --- 3. verify, from observed state -------------------------------------------

Set-Step "verify-binaries"
foreach ($exe in @($Keld, $Agent)) {
  if (-not (Test-Path $exe)) { Die "no $exe - the installer placed no binary there" }
  # RUNNING it, not Test-Path: a wrong-architecture build is present and cannot
  # execute, which is why update/ runs --version on a staged binary before any
  # swap.
  $v = (& $exe --version 2>&1 | Select-Object -First 1)
  if ($LASTEXITCODE -ne 0 -or -not $v) { Die "$exe exists but would not run: $v" }
  Ok "$exe - $v"
}

Set-Step "verify-service"
# The platform's own service manager, not a file: on Windows the registration IS
# the scheduled task. `schtasks /Query` is the fallback for a PowerShell without
# the ScheduledTasks module.
$task = Get-ScheduledTask -TaskName "KeldAgent" -ErrorAction SilentlyContinue
if ($task) {
  $info = Get-ScheduledTaskInfo -TaskName "KeldAgent" -ErrorAction SilentlyContinue
  Ok "Task Scheduler holds KeldAgent (state=$($task.State), lastResult=$($info.LastTaskResult))"
} else {
  $q = & schtasks /Query /TN KeldAgent 2>&1
  if ($LASTEXITCODE -ne 0) { Die "no KeldAgent task: $q" }
  Ok "schtasks /Query KeldAgent succeeded"
}

if (-not $SkipOnboarding) {
  Set-Step "verify-onboarded"
  # THE one fact that says this machine will collect anything. onboard.cmd
  # already states the rule for the human path - "claim success only if it is
  # true: setup is done when an ingest token exists in hook.json" - and this is
  # the same rule for the unattended one.
  $home_ = if ($env:KELD_HOME) { $env:KELD_HOME } else { Join-Path $env:USERPROFILE ".keld" }
  $hook = Join-Path $home_ "hook.json"
  if (-not (Test-Path $hook)) { Die "no $hook - onboarding never wrote one, so the daemon will idle on awaitConfig forever" }
  $token = ""
  try { $token = (Get-Content $hook -Raw | ConvertFrom-Json).ingest_token } catch { $token = "" }
  if (-not $token) { Die "$hook carries no ingest_token - the machine is installed but NOT set up, and nothing is being collected" }
  Ok "$hook holds an ingest token ($($token.Length) chars, not printed)"
}

Set-Step "report-sidecar"
# Windows BUNDLES the sidecar in the Inno payload (`ignoreversion recursesubdirs`),
# so unlike macOS there is nothing to fetch afterwards and its absence is a real
# defect rather than a timing one.
$sidecar = Join-Path $App "keld-agent-sidecar.exe"
if (Test-Path $sidecar) {
  $ver = "no VERSION - predates the stamp"
  $vf = Join-Path $App "VERSION"
  if (Test-Path $vf) { $ver = (Get-Content $vf -Raw).Trim() }
  Ok "sidecar at $sidecar ($ver)"
} else {
  Die "no $sidecar - the Inno payload bundles the sidecar, so a missing one is not a pending download"
}

if ($StopService) {
  Set-Step "stop-service"
  & schtasks /End /TN KeldAgent 2>&1 | Out-Null
  Ok "daemon stopped; the KeldAgent task stays registered"
}

Write-Host ""
Say "OK - installed by keld-setup.exe /VERYSILENT, onboarded by command, verified from observed state"
Say "bin_dir=$App"
