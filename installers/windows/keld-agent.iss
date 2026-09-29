; Inno Setup script — build in CI: iscc installers\windows\keld-agent.iss
; Per-user install (no admin). Files staged next to this script by CI:
;   keld.exe, keld-agent.exe, keld-wizard-host.exe, keld-agent-sidecar\  (frozen one-dir)
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

[Run]
; ONE ENTRY: open Signal when an interactive install finishes.
;
; ⚠️ REGISTERING THE AGENT DOES NOT LIVE HERE AND MUST NOT COME BACK. It runs in
;    CurStepChanged(ssPostInstall), through RunQuiet, unconditionally — see there.
;    `Flags: runhidden` hides keld-agent's WINDOW without stopping Windows
;    allocating a console, and on a real install that console appeared ("it pops
;    a terminal window open twice"). A [Run] entry cannot pass CREATE_NO_WINDOW;
;    keld-wizard-host --run can. Re-adding it here would also register twice.
;
; ⚠️ THIS REPLACED onboard.cmd (2026-09-29, web sign-in spec AC-10 / D10). That
;    console asked for a setup code; no installer asks anything about Keld now.
;    Signal asks the one real question on first open — sign in with Atlas, or use
;    locally only — and the daemon's auto-setup configures the AI tools it finds.
;
; `postinstall`: a checkbox on the Finished page, TICKED by default — opening
;    Signal is how an interactive install ends, not a surprise.
; `skipifsilent`: an MDM /SILENT or /VERYSILENT push opens nothing on a screen
;    nobody is at. Such a machine is paired with `keld-agent install --code
;    <CODE>` from the management tool, exactly as before.
; `shellexec`, `nowait`: the installer must not wait on a browser.
;
; ⚠️ DO NOT ADD runhidden. onboard.cmd once ran as `runhidden nowait` — an
;    interactive login in a window nobody could see — and every Windows machine
;    registered its task and then idled forever, collecting nothing and saying
;    nothing. `keld signal open` prompts for nothing, but the flag on this line
;    is how that defect arrived; keep it off.
;
; `keld signal open` needs the NEW daemon's agent.json (port + per-start
; secret), which is why ssPostInstall waits for it (WaitForAgent) before the
; Finished page appears.
Filename: "{app}\keld.exe"; Parameters: "signal open"; Description: "Open Keld Signal"; \
  Flags: postinstall shellexec skipifsilent nowait

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
Filename: "{sys}\taskkill.exe"; \
  Parameters: "/F /T /IM keld-agent-sidecar.exe /IM keld-agent.exe"; \
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
  // /T so the sidecar's own children go with it; naming both images in one call
  // matches the idiom already in [UninstallRun].
  Exec(ExpandConstant('{sys}\taskkill.exe'),
       '/F /T /IM keld-agent-sidecar.exe /IM keld-agent.exe',
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
