; Inno Setup script — build in CI: iscc installers\windows\keld-agent.iss
; Per-user install (no admin). Files staged next to this script by CI:
;   keld.exe, keld-agent.exe, keld-agent-sidecar\  (frozen one-dir)
; onboard.cmd is committed beside this script, not staged by CI.
; KELD_VERSION is set in the environment by CI.
#define MyVersion GetEnv("KELD_VERSION")

[Setup]
AppName=Keld
AppVersion={#MyVersion}
DefaultDirName={localappdata}\Programs\keld
PrivilegesRequired=lowest
DisableProgramGroupPage=yes
OutputBaseFilename=keld-setup
ChangesEnvironment=yes
LicenseFile=..\resources\EULA.txt
InfoBeforeFile=..\resources\SECURITY-OVERVIEW.txt

[Files]
Source: "keld.exe";             DestDir: "{app}"; Flags: ignoreversion
Source: "keld-agent.exe";       DestDir: "{app}"; Flags: ignoreversion
Source: "keld-agent-sidecar\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "onboard.cmd";          DestDir: "{app}"; Flags: ignoreversion

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
; TWO ENTRIES, AND BOTH ARE LOAD-BEARING. The first ALWAYS runs; the second is the
; human's onboarding and may legitimately not run at all.
;
; 1. REGISTER THE AGENT UNCONDITIONALLY. Told explicitly that no human is
;    reachable (--headless, see below), keld-agent install writes the v2
;    agent-config.json, registers the logon task, starts the daemon, and prompts
;    for nothing. The daemon then IDLES on awaitConfig until someone completes
;    setup, which is a documented, supported state — not a crash.
;
;    ⚠️ This entry exists because putting registration behind the postinstall
;    checkbox was a REGRESSION. `postinstall` renders a tickbox the user can
;    untick, and `skipifsilent` skips it entirely — so an MDM /SILENT push
;    installed the files and registered NOTHING, where even the previous broken
;    hidden step at least created the logon task. A silent-install fleet would
;    have gone dark with no error anywhere.
;
;    It waits (no `nowait`) so registration is finished before onboarding can run
;    `keld-agent install` again; two concurrent service installs would race on
;    schtasks. The headless branch runs no interactive step, so there is nothing
;    for it to block on.
;
;    ⚠️ `--headless` IS LOAD-BEARING AND MUST NOT BE DROPPED. This entry used to
;    read `Parameters: "install"` and rely on keld-agent detecting the absence of
;    a terminal by itself. IT CANNOT, HERE: `runhidden` hides the WINDOW, it does
;    not take the console away, so the child still owns a real console, stdout is
;    still a console handle, and term.IsTerminal answers TRUE. install therefore
;    took its INTERACTIVE branch inside an invisible window — it ran `keld login`
;    where nobody could see it, then `keld signal setup`, which blocked forever on
;    a [Y/n] prompt reading a stdin no human could type into.
;
;    Measured on a real machine: the installer sat at "Registering the Keld
;    agent..." indefinitely, with `keld.exe signal setup` alive as a child of
;    keld-agent.exe; killing that process by hand was the only way to advance the
;    install, and onboarding then asked for a login a SECOND time, because the
;    first had already been consumed by the hidden console.
;
;    So the intent is stated rather than inferred: --headless writes the config,
;    registers the task, starts the daemon, and prompts for NOTHING. macOS and
;    Linux are unaffected — they never pass it, and their launchers really do
;    detach stdio.
Filename: "{app}\keld-agent.exe"; Parameters: "install --headless"; \
  StatusMsg: "Registering the Keld agent..."; Flags: runhidden

; 2. ONBOARD THE HUMAN, in a VISIBLE console in their session: prompt for the
;    one-time code, redeem it, configure the tools.
;
;    ⚠️ DO NOT ADD runhidden TO THIS LINE. It used to be `keld-agent.exe install`
;    with `runhidden nowait`, which meant the interactive login ran where nobody
;    could see or complete it, on a step Inno neither waited for nor could report.
;    Every Windows machine registered its task and then idled forever, collecting
;    nothing and saying nothing.
;
;    skipifsilent is correct HERE and only here: a /SILENT push must not block on a
;    console waiting for a human. Such a machine is registered by entry 1 and
;    finished with `keld-agent install --code <CODE>` from the management tool.
Filename: "{app}\onboard.cmd"; Description: "Set up Keld"; \
  Flags: postinstall shellexec skipifsilent

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
// HIDE THE PER-FILE LABEL ON THE INSTALLING PAGE.
//
// Inno writes the full path of each file it extracts into WizardForm.FilenameLabel,
// under the progress bar. For a normal installer that is a handful of lines; this
// payload is the FROZEN SIDECAR — roughly 15,000 files of torch and transformers —
// so it becomes a blur of unfamiliar deep paths (`_internal\torch\_vendor\quack\
// _compile_worker.py` and thousands like it) scrolling past for minutes. It reads
// like something rummaging through the machine, which is precisely the impression an
// on-device privacy product must not give.
//
// Only the LABEL is hidden. The progress bar still moves and the status line above it
// still says what is happening ("Extracting files..."), so nothing about progress or
// duration is concealed — what goes is a firehose of filenames nobody can act on.
procedure InitializeWizard();
begin
  WizardForm.FilenameLabel.Visible := False;
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
  Home := GetEnv('KELD_HOME');
  if Home = '' then
    Home := ExpandConstant('{%USERPROFILE}\.keld');
  if not DirExists(Home) then
    exit;
  if MsgBox('Also remove your Keld settings and credentials?' #13#10#13#10
            + Home + #13#10#13#10
            + 'Choose No to keep them, so re-installing will not ask you to log in again.',
            mbConfirmation, MB_YESNO or MB_DEFBUTTON2) = IDYES then
    DelTree(Home, True, True, True);
end;
