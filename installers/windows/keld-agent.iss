; Inno Setup script — build in CI: iscc installers\windows\keld-agent.iss
; Per-user install (no admin). Files staged next to this script by CI:
;   keld.exe, keld-agent.exe, keld-wizard-host.exe, keld-agent-sidecar\  (frozen one-dir),
;   and Keld Signal.exe (the desktop app; optional — see [Files])
; This installer only installs: it asks nothing about Keld (no sign-in, no setup
; code, no tool picker) and opens Signal at the end of an interactive install.
; See docs/superpowers/specs/2026-09-29-signal-web-signin-discovery.html, AC-10.
; KELD_VERSION is set in the environment by CI.
#define MyVersion GetEnv("KELD_VERSION")

[Setup]
AppName=Keld
AppVersion={#MyVersion}
; Publisher and file metadata. Windows shows these in the file's Properties and
; in Add/Remove Programs, and an installer carrying none of them looks anonymous
; to both a person and to reputation-based gates.
;
; ⚠️ THIS IS NOT A SUBSTITUTE FOR SIGNING, and must not be mistaken for one.
; keld-setup.exe is unsigned, and Smart App Control — on by default on clean
; Windows 11 — blocks unsigned installers outright, with a dialog and no log
; line. Observed on a dev machine 2026-09-15: some builds of this very installer
; ran and others were blocked, because the verdict is per file hash. Metadata
; makes the file honest about its origin; only an Authenticode signature makes it
; reliably runnable.
AppPublisher=Keld
AppPublisherURL=https://keld.co
AppSupportURL=https://keld.co
VersionInfoCompany=Keld
VersionInfoProductName=Keld Signal
VersionInfoDescription=Keld Signal installer
VersionInfoCopyright=Keld
DefaultDirName={localappdata}\Programs\keld
PrivilegesRequired=lowest
DisableProgramGroupPage=yes
OutputBaseFilename=keld-setup
; ⚠️ THE UNINSTALLER IS THE ONE BINARY NO BUILD STEP CAN REACH. Inno extracts
; unins000.exe onto the target machine at install time, so signing the payload
; and signing keld-setup.exe both miss it — and an unsigned uninstaller is
; blocked on exactly the machines installing was blocked on, leaving people
; unable to remove the product. `SignedUninstaller` is what covers it: Inno
; signs the uninstaller with the SignTool configured below.
;
; ⚠️ BOTH DIRECTIVES ARE GATED, BECAUSE EITHER ONE ALONE FAILS THE COMPILE.
; Measured: with `SignedUninstaller=yes` and no SignTool, iscc HALTS with
; "Signed uninstaller mode is enabled … please attach your digital signature",
; which would take the Windows build down on every fork and dry run — the exact
; thing the macOS job's no-secrets path is careful to avoid. The preprocessor
; check keeps an unsigned build byte-identical to today's.
;
; ⚠️ THIS IS NOW WIRED, AND THE MECHANISM IS DELIBERATELY NOT THE ONE USED FOR
; EVERYTHING ELSE. The payload and keld-setup.exe are signed by
; `Azure/artifact-signing-action`, but an Action cannot be what iscc shells out
; to — `SignTool` needs a COMMAND LINE, and the uninstaller stub exists only
; during this compile, so no before-or-after step can reach it.
;
; Microsoft ships the same service as a signtool plugin for exactly this case:
; `Microsoft.Trusted.Signing.Client` carries bin\x64\Azure.CodeSigning.Dlib.dll,
; authenticating with the SAME four AZURE_* credentials (it bundles
; Azure.Identity, so DefaultAzureCredential reads them from the environment).
; The workflow's "Prepare the uninstaller signer" step builds the command and
; exports it as KELD_SIGN_COMMAND; iscc receives it as /Skeldsign=.
;
; ⚠️ signtool must come from Windows SDK 10.0.22621.0 or newer — the vendor's own
; README requires it — which is why the workflow installs SDK BuildTools rather
; than using whatever signtool happens to be on the runner.
;
; CI supplies the tool with `iscc /Skeldsign=<command with $f>`; see
; .github/workflows/installers.yml.
#if GetEnv("KELD_SIGN_COMMAND") != ""
SignTool=keldsign
SignedUninstaller=yes
#endif
; ⚠️ **BOTH OF THESE MUST STAY `no`, AND THE DEFAULTS ARE WRONG FOR US.**
;
; Inno defaults to CloseApplications=yes, which hands a locked file to the
; Restart Manager and shows the person a modal listing the processes it wants to
; close. On an upgrade that fires every time, and it reads as an ERROR rather
; than a routine step — reported as "a warning message… but it looks too much
; like an error". Nothing about replacing our own daemon needs a question asked.
;
; RestartApplications=yes is worse, and is where the terminal came from: after
; installing, Inno RELAUNCHES whatever it closed. keld-agent.exe is a CONSOLE
; binary and Inno is a GUI process with no console, so Windows gives the
; relaunched daemon a brand-new console window and shows it — the same defect
; fixed in three other places this release. It also relaunches the daemon
; WITHOUT `--hide-console` and outside the scheduled task, so the process it
; starts is not the one the task manages.
;
; PrepareToInstall (see [Code]) stops the task and the processes itself, quietly,
; BEFORE any file is replaced — so nothing is locked, no modal is needed, and
; nothing has to be restarted by Inno. The [Run] entry re-registers and starts
; the agent afterwards, which is the path that passes --hide-console.
CloseApplications=no
RestartApplications=no
ChangesEnvironment=yes
LicenseFile=..\resources\EULA.txt
InfoBeforeFile=..\resources\SECURITY-OVERVIEW.txt

[Files]
Source: "keld.exe";             DestDir: "{app}"; Flags: ignoreversion
Source: "keld-agent.exe";       DestDir: "{app}"; Flags: ignoreversion
Source: "keld-wizard-host.exe"; DestDir: "{app}"; Flags: ignoreversion
; The Keld Signal desktop app. `keld signal open` prefers it over a browser tab
; and finds it by looking BESIDE keld.exe — which is why it installs here rather
; than into its own directory: one install dir, and nothing to keep in step.
;
; ⚠️ `skipifsourcedoesntexist` IS LOAD-BEARING. The app is a convenience: without
; it `signal open` opens a browser, which is what every release before this did.
; A Rust build that fails on the runner must not take the whole Windows installer
; down with it, and a missing `Source:` is otherwise a COMPILE ERROR.
Source: "Keld Signal.exe"; DestDir: "{app}"; Flags: ignoreversion skipifsourcedoesntexist
Source: "keld-agent-sidecar\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Registry]
; PATH IS ADDED UNCONDITIONALLY — there is deliberately no [Tasks] checkbox for it.
; It used to be `Name: "addtopath"; Description: "Add Keld to my PATH"`, opt-out via
; a tickbox on a "Select Additional Tasks" page. That page asked the user to make a
; decision they have no way to evaluate, and getting it wrong is silent: `keld` and
; `keld-agent` are then not on PATH, so every instruction this installer prints —
; `keld login`, `keld signal setup`, `keld signal doctor` — fails with "not
; recognized" and the machine looks broken rather than unconfigured.
;
; Removing the only [Tasks] entry also removes the wizard page it lived on, which is
; the point: one fewer question between the user and a working install.
;
; `Check: NeedsAddPath` still guards it, and that is the part that must not be
; dropped — it is what makes a RE-INSTALL idempotent. Without it {app} is appended
; again on every run and PATH grows without bound.
Root: HKCU; Subkey: "Environment"; ValueType: expandsz; ValueName: "Path"; \
  ValueData: "{olddata};{app}"; Check: NeedsAddPath('{app}')

[Icons]
; THE ONLY WAY TO REACH THE APP ONCE THE INSTALLER IS GONE. Before this entry the
; product shipped a desktop app with no entry point: `DisableProgramGroupPage=yes`
; and no [Icons] section at all, so after the Finished page closed the app could
; only be started by `keld signal open` from a terminal — on a product whose whole
; Windows story is that no terminal ever appears — or by finding the exe under
; {localappdata}. The postinstall launch entry in [Run] opens it exactly once,
; which hides the gap rather than closing it.
;
; ⚠️ `{userprograms}`, NOT `{group}`, AND NOT BECAUSE THEY DIFFER IN PERMISSION.
; This is a per-user install (PrivilegesRequired=lowest,
; DefaultDirName={localappdata}\Programs\keld) so both land in the same Start
; Menu. The difference is shape: `{group}` is a FOLDER, named by
; DefaultGroupName, and with DisableProgramGroupPage=yes nobody ever sees or
; chooses it — so a single shortcut would sit alone inside a folder called
; "Keld", which Windows 11's All apps list renders as a collapsed group a person
; has to expand to find the one thing in it. One app, one entry, no folder.
;
; ⚠️ THE `Check` IS THE SAME OPTIONALITY [Files] AND [Run] ALREADY CARRY, AND
; WITHOUT IT A BUILD WITHOUT THE APP SHIPS A DEAD SHORTCUT. The Rust step is
; `continue-on-error` on the runner, so `Keld Signal.exe` may legitimately be
; absent (see skipifsourcedoesntexist in [Files] and skipifdoesntexist in [Run]).
; [Icons] has no `skipifdoesntexist` — an entry whose target is missing is
; created anyway, pointing at nothing — so the guard has to be a Check, and it
; runs after [Files], which is what makes FileExists the right question.
;
; Inno logs every shortcut it creates and removes it on uninstall, so this needs
; no [UninstallDelete] companion.
Name: "{userprograms}\Keld Signal"; Filename: "{app}\Keld Signal.exe"; \
  Comment: "Your focus blocks, your projects, and whether they reached Atlas"; \
  Check: AppPresent

[Run]
; OPEN SIGNAL WHEN AN INTERACTIVE INSTALL FINISHES — EXACTLY ONE OF TWO ENTRIES.
;
; The desktop app when it is on disk, and `keld signal open` (a browser tab) when
; it is not. The two Checks are MUTUALLY EXCLUSIVE (`AppPresent` / `not
; AppPresent`) so the Finished page shows one "Open Keld Signal" checkbox and
; opens one window. Postinstall Checks are evaluated when the Finished page is
; built, after [Files], so FileExists is the right question (see AppPresent).
;
; ⚠️ REGISTERING THE AGENT DOES NOT LIVE HERE AND MUST NOT COME BACK. It runs in
;    CurStepChanged(ssPostInstall), through RunQuiet, unconditionally — see there.
;    `Flags: runhidden` hides keld-agent's WINDOW without stopping Windows
;    allocating a console, and on a real install that console appeared ("it pops
;    a terminal window open twice"). A [Run] entry cannot pass CREATE_NO_WINDOW;
;    keld-wizard-host --run can. Re-adding it here would also register twice.
;
; ⚠️ THESE REPLACED onboard.cmd (2026-09-29, web sign-in spec AC-10 / D10). That
;    console asked for a setup code; no installer asks anything about Keld now.
;    Signal asks the one real question on first open — sign in with Atlas, or use
;    locally only — and the daemon's auto-setup configures the AI tools it finds.
;
; Both are `postinstall` (a checkbox on the Finished page) and TICKED by default:
;    opening Signal is how an interactive install ends, not a surprise. Both are
;    `skipifsilent`: an MDM /SILENT or /VERYSILENT push opens nothing on a screen
;    nobody is at, and is paired with `keld-agent install --code <CODE>` from the
;    management tool, exactly as before.
;
; ⚠️ NEITHER IS runhidden. onboard.cmd once ran as `runhidden nowait` — an
;    interactive login in a window nobody could see — and every Windows machine
;    registered its task and then idled forever. On the app entry it would also
;    hide the very window the entry exists to open.
;
; Both need the NEW daemon's agent.json (port + per-start secret): ssPostInstall
; waits for it (WaitForAgent) before the Finished page appears. If that wait
; runs out, the app still recovers on its own — `follow_agent` in
; app/src-tauri/src/main.rs polls every 2s and navigates once the file appears.

; 1. THE DESKTOP APP, when this build shipped it.
;
;    `Keld Signal.exe` is a GUI-subsystem binary (verified: PE subsystem 2), so
;    CreateProcess allocates it no console at all — none of this file's console
;    defences apply, and none are needed.
;
;    ⚠️ `skipifdoesntexist` IS LOAD-BEARING even beside the Check: the app's Rust
;    build is `continue-on-error` on the runner, and Inno reports a HARD ERROR
;    when it cannot start a [Run] command. A missing app must be a no-op.
Filename: "{app}\Keld Signal.exe"; Description: "Open Keld Signal"; \
  Check: AppPresent; Flags: postinstall nowait skipifsilent skipifdoesntexist

; 2. THE FALLBACK: `keld signal open`, when there is no app on disk. It opens the
;    page in the default browser. `shellexec` + `nowait`: the installer must not
;    wait on a browser. `keld.exe` is a console binary, so this likely flashes a
;    console for the moment it runs — unmeasured; only builds without the app
;    reach it.
Filename: "{app}\keld.exe"; Parameters: "signal open"; Description: "Open Keld Signal"; \
  Check: not AppPresent; Flags: postinstall shellexec skipifsilent nowait

[UninstallRun]
; UNINSTALL USED TO REMOVE THE FILES AND NOTHING ELSE, which left three things
; behind on every machine — each of them silent, and the first two actively broken.
;
; 1. THE TOOL CONFIGS. `keld signal setup` points Claude Code / Codex / Gemini at
;    the daemon's loopback OTLP proxy (127.0.0.1:14318). Delete the daemon and
;    that config survives, so the tools go on posting telemetry at a port nothing
;    answers — for as long as the machine lives. Restoring them is what
;    `keld signal uninstall` is FOR, and nothing was calling it.
;
; 2. THE SCHEDULED TASK. `keld-agent install` registers a KeldAgent logon task;
;    with no [UninstallRun] it outlived the uninstall, pointing at a deleted exe.
;    Add/Remove Programs then said Keld was gone while Task Scheduler still had a
;    KeldAgent entry failing at every logon.
;
; 3. A RUNNING DAEMON HOLDING ITS OWN FILES OPEN. Windows will not delete a
;    running exe, and the frozen sidecar is ~15,000 files under {app} — so a live
;    keld-agent.exe or keld-agent-sidecar.exe could make the uninstall fail
;    halfway and leave a half-removed directory.
;
; ORDER IS LOAD-BEARING AND IS THE ORDER OF THESE LINES. Entry 1 needs the
; manifest under ~/.keld, which the [Code] step below may remove; both need their
; binaries, which Inno deletes only AFTER this section runs.
;
; `skipifdoesntexist` on the two Keld entries: a partially-completed earlier
; uninstall leaves no binary, and Inno reports a hard error when it cannot START a
; command. Missing binaries must be a no-op, never a failed uninstall.
Filename: "{app}\keld.exe"; Parameters: "signal uninstall --yes"; \
  Flags: runhidden skipifdoesntexist; RunOnceId: "restoretools"
Filename: "{app}\keld-agent.exe"; Parameters: "uninstall"; \
  Flags: runhidden skipifdoesntexist; RunOnceId: "deregister"
; The backstop, and deliberately by IMAGE NAME. The entry above ends the scheduled
; task, which covers the daemon it started — but not a sidecar child orphaned by
; that kill (Windows has no SIGTERM path to the daemon's own group-reaping
; teardown), and not a keld-agent someone launched by hand. taskkill exits non-zero
; when nothing matches, which Inno ignores for this section, so "already gone" is a
; normal outcome rather than an error.
;
; ⚠️ "Keld Signal.exe" IS HERE FOR THE SAME REASON IT IS IN PrepareToInstall:
; Windows will not delete a running exe, and the app outlives its own window
; (closing it hides it behind a tray icon), so an uninstall started from a
; machine where anyone ever opened it would fail to remove the app. `/F` rather
; than a graceful close for the same reason too — the app's CloseRequested
; handler calls prevent_close, so WM_CLOSE hides it and changes nothing.
Filename: "{sys}\taskkill.exe"; \
  Parameters: "/F /T /IM keld-agent-sidecar.exe /IM keld-agent.exe /IM ""Keld Signal.exe"""; \
  Flags: runhidden; RunOnceId: "killstragglers"

[Code]
// ── This installer asks nothing about Keld ───────────────────────────────────
//
// There used to be a "Set up Keld" page here: it redeemed a setup code or ran
// the browser device flow with Atlas's approval page embedded (keld-wizard-host
// --panel), then asked which AI tools to configure. It was removed on 2026-09-29
// (docs/superpowers/specs/2026-09-29-signal-web-signin-discovery.html, AC-10):
// installers only install. Signal asks on first open, and the daemon's own
// auto-setup (internal/agent/integrations) configures detected tools. What is
// left is install plumbing — stop the agent, register it without a console,
// wait for it to come up, and the uninstall cleanup.
//
// installers/windows/keld_agent_iss_test.sh fails if a custom page, a sign-in
// run or a tool step comes back.

var
  TraceFile: String;

// Trace appends one line to a fixed path, so a run that goes wrong leaves
// evidence rather than a description.
//
// ⚠️ It writes OUTSIDE {tmp} on purpose: Inno deletes {tmp} when the wizard
// closes, taking the helper's event files with it, so a log kept there is gone
// exactly when it is wanted. It records steps and exit codes only — no token,
// no secret, no URL query.
procedure Trace(const S: String);
var
  Existing: AnsiString;
begin
  if TraceFile = '' then
    exit;
  if not LoadStringFromFile(TraceFile, Existing) then
    Existing := '';
  SaveStringToFile(TraceFile, Existing + S + #13#10, False);
end;

procedure InitializeWizard();
begin
  // HIDE THE PER-FILE LABEL ON THE INSTALLING PAGE.
  //
  // Inno writes the full path of each file it extracts into
  // WizardForm.FilenameLabel, under the progress bar. For a normal installer that
  // is a handful of lines; this payload is the FROZEN SIDECAR — roughly 15,000
  // files of torch and transformers — so it becomes a blur of unfamiliar deep
  // paths scrolling past for minutes. It reads like something rummaging through
  // the machine, which is precisely the impression an on-device privacy product
  // must not give. Only the LABEL is hidden: the progress bar still moves and the
  // status line above it still says what is happening.
  WizardForm.FilenameLabel.Visible := False;

  TraceFile := ExpandConstant('{%TEMP}\keld-wizard-trace.log');
  DeleteFile(TraceFile);
end;

// The [Icons] entry's Check. The desktop app is optional — its Rust build is
// continue-on-error on the runner — and [Icons] has no `skipifdoesntexist`, so
// without this a build that shipped no app would still create a Start Menu
// shortcut pointing at a file that does not exist.
//
// ⚠️ THIS IS ONLY CORRECT BECAUSE [Icons] RUNS AFTER [Files]. Asked any earlier
// — from PrepareToInstall, say — it would answer False on every FRESH install,
// because {app}\Keld Signal.exe is not there yet, and the shortcut would be
// created only on upgrades. Inno processes [Icons] once the payload is on disk,
// which is what makes FileExists the question rather than a guess about what CI
// built.
function AppPresent: Boolean;
begin
  Result := FileExists(ExpandConstant('{app}\Keld Signal.exe'));
end;

// PrepareToInstall stops the running agent BEFORE any file is replaced.
//
// ⚠️ THIS IS WHAT REPLACES THE RESTART-MANAGER MODAL. With CloseApplications=no
// nothing else will free the locked binaries, so an upgrade over a running
// daemon would fail to replace keld-agent.exe and the sidecar. Doing it here is
// also the only way it happens QUIETLY: Inno's dialog asks a question that has
// exactly one sensible answer.
//
// ⚠️ SW_HIDE IS REQUIRED ON BOTH CALLS. schtasks and taskkill are console
// programs and Setup is a GUI process with no console, so an unhidden call
// gives each one a new console window — the defect this release fixes in the
// service package, the daemon and the postinstall action. Same rule here.
//
// Failures are deliberately IGNORED. A first install has no task and no running
// process, so both commands return non-zero for the ordinary case; a real
// inability to stop the daemon surfaces immediately afterwards as a file-in-use
// error from the copy step, which reports the actual blocked path rather than a
// guess made here.
// RemoveStaleUninstaller deletes an existing uninstaller EXE so this install
// writes the current — signed — one in its place. See the call site for why Inno
// otherwise keeps it forever.
//
// Refuses in the two cases where the slot is not clearly ours: no EXE (nothing
// to do) and no matching .dat (the log is what binds unins000 to this AppId, and
// without it Inno may choose a different number, which would leave Add/Remove
// pointing at a file that never appears).
procedure RemoveStaleUninstaller;
var
  Exe, Dat: String;
begin
  Exe := ExpandConstant('{app}\unins000.exe');
  Dat := ExpandConstant('{app}\unins000.dat');
  if not FileExists(Exe) then
    exit;
  if not FileExists(Dat) then
  begin
    Trace('uninstaller: no unins000.dat, leaving the existing exe alone');
    exit;
  end;
  if DeleteFile(Exe) then
    Trace('uninstaller: removed the stale exe so a signed one is written')
  else
    // Not fatal: the install proceeds and the old uninstaller stays. Saying so
    // is the point — a silent failure here is indistinguishable from the bug.
    Trace('uninstaller: could NOT remove the stale exe; it will be kept unsigned');
end;

function PrepareToInstall(var NeedsRestart: Boolean): String;
var
  RC: Integer;
begin
  Result := '';
  NeedsRestart := False;
  Trace('PrepareToInstall: stopping the agent');
  Exec(ExpandConstant('{sys}\schtasks.exe'), '/End /TN KeldAgent',
       '', SW_HIDE, ewWaitUntilTerminated, RC);
  // /T so the sidecar's own children go with it; naming every image in one call
  // matches the idiom already in [UninstallRun].
  //
  // ⚠️ "Keld Signal.exe" IS IN THIS LIST BECAUSE THE INSTALLER NOW LAUNCHES IT,
  // AND WITHOUT IT THE *NEXT* INSTALL FAILS. Windows refuses to delete a running
  // exe, so the copy step stops on a modal: "An error occurred while trying to
  // replace the existing file: DeleteFile failed; code 5. Access is denied."
  // Measured on a real machine, first install after the postinstall launch entry
  // was added. Nothing else here covers it — `CloseApplications=no` is set
  // deliberately (the Restart Manager modal it renders reads as an error and
  // failed to stop the agent anyway), so Inno will not offer to close it.
  //
  // ⚠️ AND `/F` IS LOAD-BEARING FOR THIS IMAGE SPECIFICALLY. A polite taskkill
  // posts WM_CLOSE, which the app INTERCEPTS: its CloseRequested handler calls
  // prevent_close and hides the window (see app/src-tauri/src/main.rs — closing
  // the window is not quitting, so re-showing it is instant). So a graceful kill
  // is a no-op that leaves the process alive and the file locked, and it is the
  // same design that makes this necessary at all: a person who "closed" the app
  // still has it running behind a tray icon with no idea it is there.
  Exec(ExpandConstant('{sys}\taskkill.exe'),
       '/F /T /IM keld-agent-sidecar.exe /IM keld-agent.exe /IM "Keld Signal.exe"',
       '', SW_HIDE, ewWaitUntilTerminated, RC);
  // A moment for the OS to release the file handles the copy is about to take.
  Sleep(600);

  // ⚠️ **INNO KEEPS AN EXISTING unins000.exe ON UPGRADE, SO THE SIGNED ONE NEVER
  // LANDS ON A MACHINE THAT ALREADY HAS AN UNSIGNED ONE.** Measured: v11
  // replaced every binary in {app} at 14:35 and left unins000.exe untouched from
  // 11:10, still NotSigned — while CI had proved the uninstaller stub inside
  // keld-setup.exe WAS signed. The uninstaller is only written when it is
  // missing (or older), and both were produced by the same Inno version, so
  // nothing about the file looked stale to it.
  //
  // The consequence is the one this whole signing exercise exists to remove:
  // installing works and UNINSTALLING is refused, on precisely the machines that
  // upgraded — which is every beta tester who took a build before signing landed.
  //
  // ⚠️ THE .dat IS WHAT CLAIMS THE SLOT, NOT THE .exe. Inno finds the existing
  // uninstall log for this AppId and reuses its number, so deleting only the
  // EXE leaves unins000 bound to this install and Inno writes a fresh one there.
  // Deleting the .dat WOULD orphan the log and strand every file recorded in it,
  // so it is deliberately left alone — and if the log is absent, nothing is
  // touched at all, because then the slot is not ours to assume.
  RemoveStaleUninstaller;
end;

// RunQuiet runs a console program with NO CONSOLE WINDOW AT ALL, and waits.
//
// ⚠️ **Inno's own `Exec(…, SW_HIDE, …)` AND `Flags: runhidden` ARE NOT ENOUGH,
// AND THIS IS THE FOURTH PLACE THAT HAS BEEN TRUE.** Both set
// STARTF_USESHOWWINDOW/SW_HIDE, which asks a window not to be shown once it
// exists; the console is still allocated, and on a real install two of them
// appeared anyway — reported as "after it finishes the progress bar for
// installing, it pops a terminal window open twice". CREATE_NO_WINDOW is what
// stops the console being created, and Pascal Script cannot pass it.
//
// keld-wizard-host can: it is built -H windowsgui and applies CREATE_NO_WINDOW
// to every child (cmd/keld-wizard-host/nowindow_windows.go). The installer's
// former wizard page drove four or five `keld` runs through it with no flash at
// all, which is the evidence this is the mechanism that works here. `--run`
// waits for the child and returns ITS exit code, so this stays synchronous.
//
// Falls back to a direct Exec if the helper is missing, because an install that
// skips these steps is worse than one that flashes.
function RunQuiet(const Exe, Args, EvDir: String): Integer;
var
  Host, Params: String;
  RC: Integer;
begin
  Result := -1;
  Host := ExpandConstant('{app}\keld-wizard-host.exe');
  if not FileExists(Host) then
  begin
    Trace('RunQuiet: no helper, falling back to Exec for ' + Exe);
    if Exec(Exe, Args, '', SW_HIDE, ewWaitUntilTerminated, RC) then
      Result := RC;
    exit;
  end;
  ForceDirectories(EvDir);
  Params := '--run "' + Exe + '" --events-dir "' + EvDir + '" -- ' + Args;
  if Exec(Host, Params, '', SW_HIDE, ewWaitUntilTerminated, RC) then
    Result := RC;
  Trace('RunQuiet ' + Exe + ' rc=' + IntToStr(Result));
end;

// KeldHome is where the daemon reads and writes. KELD_HOME is honoured, because
// assuming %USERPROFILE%\.keld would watch (or offer to delete) a directory that
// is not the one in use.
function KeldHome: String;
begin
  Result := GetEnv('KELD_HOME');
  if Result = '' then
    Result := ExpandConstant('{%USERPROFILE}\.keld');
end;

// WaitForAgent waits, bounded, for the daemon just started to write agent.json.
//
// ⚠️ `keld signal open` — the Finished page's "Open Keld Signal" — reads that
// file for the page's port and its per-start secret. On a FIRST install it does
// not exist until the daemon writes it, so opening too early prints "not
// running" into a console that closes at once: the person sees nothing open. On
// an UPGRADE it holds the PREVIOUS daemon's port and secret (PrepareToInstall
// killed that daemon), so opening too early lands on a dead port. Either way the
// one thing an interactive install now ends with does not happen. So wait for
// the content to CHANGE from what it was before registration, not merely exist.
//
// Bounded because Pascal Script cannot pump messages while it sleeps: a daemon
// that never starts must not wedge the wizard. Normally this returns in well
// under a second. Past the bound, the Finished page still offers the step, and
// the app/page recovers on its own once the daemon is up.
procedure WaitForAgent(const Before: AnsiString);
var
  Path: String;
  Cur: AnsiString;
  I: Integer;
begin
  Path := AddBackslash(KeldHome) + 'agent.json';
  for I := 1 to 40 do
  begin
    if LoadStringFromFile(Path, Cur) and (Cur <> '') and (Cur <> Before) then
    begin
      Trace('agent.json written after ' + IntToStr(I * 250) + ' ms');
      exit;
    end;
    Sleep(250);
  end;
  Trace('agent.json not rewritten within 10 s; Signal may open before the agent is up');
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  RC: Integer;
  AgentBefore: AnsiString;
begin
  if CurStep <> ssPostInstall then
    exit;

  // ⚠️ NOTHING HERE DEPENDS ON ANYTHING A PERSON DID. There used to be a tool
  // step gated on the removed wizard page's pairing, and while registration sat
  // behind the same gate an MDM /SILENT push (page never runs) installed the
  // files and registered NOTHING, silently. Now registration is all there is,
  // and keld_agent_iss_test.sh fails if a pairing state returns here.
  if not LoadStringFromFile(AddBackslash(KeldHome) + 'agent.json', AgentBefore) then
    AgentBefore := '';

  // Registering starts the daemon UNPAIRED: it collects, holds what it would
  // send, and serves the page, where Signal asks whether to sign in. An MDM
  // machine is paired afterwards with `keld-agent install --code <CODE>`.
  //
  // ⚠️ `--headless` IS LOAD-BEARING AND MUST NOT BE DROPPED. Without it,
  // keld-agent detects a console (it HAS one — hiding a window does not take the
  // console away, so term.IsTerminal answers true) and takes its INTERACTIVE
  // branch: it runs `keld login` where nobody can see it, then `keld signal
  // setup`, which blocks forever on a [Y/n] prompt reading a stdin no human can
  // type into. Measured on a real machine: the install sat at "Registering the
  // Keld agent..." indefinitely and killing the child by hand was the only way
  // on. Through RunQuiet there is no console at all, which makes the flag's
  // job easier rather than unnecessary — keep it.
  WizardForm.StatusLabel.Caption := 'Registering the Keld agent...';
  RC := RunQuiet(ExpandConstant('{app}\keld-agent.exe'), 'install --headless',
                 ExpandConstant('{tmp}\post-agent'));
  if RC <> 0 then
    Trace('agent install rc=' + IntToStr(RC))
  else if not WizardSilent then
    // Only an interactive install opens Signal ([Run] is skipifsilent), so
    // only an interactive install waits for it.
    WaitForAgent(AgentBefore);

  // ⚠️ CONFIRM THE UNINSTALLER EXISTS, because PrepareToInstall DELETED the old
  // one and the assumption that Inno rewrites it in the same slot is exactly
  // that — an assumption. `{uninstallexe}` is Inno's own answer to "which file
  // did I choose", so this checks the real outcome rather than the expected one.
  // A missing uninstaller is recoverable (re-run the installer) and invisible
  // until someone tries to uninstall months later, which is why it is recorded
  // now rather than discovered then.
  if FileExists(ExpandConstant('{uninstallexe}')) then
    Trace('uninstaller: present at ' + ExpandConstant('{uninstallexe}'))
  else
    Trace('uninstaller: MISSING at ' + ExpandConstant('{uninstallexe}') +
          ' - re-run the installer to restore it');
end;

function NeedsAddPath(P: string): Boolean;
var
  O: string;
begin
  if not RegQueryStringValue(HKCU, 'Environment', 'Path', O) then
    O := '';
  Result := Pos(';' + P + ';', ';' + O + ';') = 0;
end;

// REMOVE {app} FROM PATH ON UNINSTALL — the mirror of the [Registry] entry above.
//
// Inno can append to PATH declaratively but cannot subtract from it: the value is
// shared, so `uninsdeletevalue` would delete the user's WHOLE Path rather than our
// segment of it. Hence string surgery, and hence it is scoped as tightly as it can
// be: HKCU only (never the system PATH), and an exact `;{app};` match.
//
// The sentinel idiom is the same one NeedsAddPath uses to decide whether to add,
// so add and remove agree by construction about what "already present" means.
// A value that does not contain our segment is left completely untouched — no
// write at all, so a PATH we never modified cannot be rewritten by an uninstall.
procedure RemoveFromPath(P: string);
var
  Cur, Stripped: string;
begin
  if not RegQueryStringValue(HKCU, 'Environment', 'Path', Cur) then
    exit;
  Stripped := ';' + Cur + ';';
  StringChangeEx(Stripped, ';' + P + ';', ';', True);
  Stripped := Copy(Stripped, 2, Length(Stripped) - 2); // strip the sentinels back off
  if Stripped <> Cur then
    RegWriteExpandStringValue(HKCU, 'Environment', 'Path', Stripped);
end;

// OFFER to remove ~/.keld, defaulting to NO.
//
// It holds credentials (auth.json, hook.json), the spool, and the reference-series
// store. Deleting it silently would destroy a login the user may not be able to
// re-obtain unattended, so this ASKS — and MB_DEFBUTTON2 makes "No" the default, so
// an uninstall driven by someone hitting Enter keeps the data.
//
// KELD_HOME is honoured, because that is where the daemon actually reads and writes;
// assuming %USERPROFILE%\.keld would prompt about a directory that is not the one in
// use and leave the real one behind.
procedure CurUninstallStepChanged(CurUninstallStep: TUninstallStep);
var
  Home: string;
begin
  if CurUninstallStep <> usPostUninstall then
    exit;
  RemoveFromPath(ExpandConstant('{app}'));
  Home := KeldHome;
  if not DirExists(Home) then
    exit;
  if MsgBox('Also remove your Keld settings and credentials?' #13#10#13#10
            + Home + #13#10#13#10
            + 'Choose No to keep them, so re-installing will not ask you to log in again.',
            mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
    DelTree(Home, True, True, True);
end;
