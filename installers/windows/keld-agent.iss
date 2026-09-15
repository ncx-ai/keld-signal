; Inno Setup script — build in CI: iscc installers\windows\keld-agent.iss
; Per-user install (no admin). Files staged next to this script by CI:
;   keld.exe, keld-agent.exe, keld-wizard-host.exe, keld-agent-sidecar\  (frozen one-dir)
; onboard.cmd is committed beside this script, not staged by CI.
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
ChangesEnvironment=yes
LicenseFile=..\resources\EULA.txt
InfoBeforeFile=..\resources\SECURITY-OVERVIEW.txt

[Files]
Source: "keld.exe";             DestDir: "{app}"; Flags: ignoreversion
Source: "keld-agent.exe";       DestDir: "{app}"; Flags: ignoreversion
Source: "keld-wizard-host.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "keld-agent-sidecar\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs
Source: "onboard.cmd";          DestDir: "{app}"; Flags: ignoreversion

; ⚠️ THE SAME TWO BINARIES AGAIN, `dontcopy`, AND BOTH ARE LOAD-BEARING.
; The "Set up Keld" page runs BEFORE the payload is installed, so {app}\keld.exe
; does not exist while the page needs it. `dontcopy` files are never installed;
; they are stored in the setup and extracted to {tmp} on demand by
; ExtractTemporaryFile. This is the Windows equivalent of the macOS wizard plugin
; carrying its own copy of `keld` in the bundle's Resources, and for exactly the
; same reason.
;
; ⚠️ The consequence is what makes `--bin-path` mandatory in CurStepChanged
; below: the page drives {tmp}\keld.exe, and pinning THAT path into a tool's
; hook command breaks every hook the moment the wizard closes — silently, since
; the config looks perfectly correct.
Source: "keld.exe";             Flags: dontcopy
Source: "keld-wizard-host.exe"; Flags: dontcopy

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
; TWO ENTRIES, AND BOTH ARE LOAD-BEARING. The first ALWAYS runs; the second is now
; a FALLBACK that should normally not run at all.
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
;    ⚠️ IT RUNS AFTER CurStepChanged(ssPostInstall), AND THAT ORDER IS THE POINT.
;    Inno runs [Run] once ssPostInstall has returned, so `keld signal setup` has
;    already written hook.json by the time the agent registers and starts — the
;    daemon comes up with a configuration to read instead of idling on
;    awaitConfig waiting for one.
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

; 2. THE CONSOLE FALLBACK, which a normal install no longer reaches.
;
;    Onboarding now happens in the "Set up Keld" wizard page (see [Code]), so this
;    entry is gated on `Check: NeedsConsoleOnboarding` — true only when the page
;    did not complete a pairing. That is a narrow set: a /SILENT push (where the
;    page never runs, and `skipifsilent` already covers it), or a [Code] failure.
;
;    ⚠️ DO NOT DELETE THIS ENTRY just because the page normally handles setup.
;    The page is the thing that can fail; this is what a machine falls back to
;    when it does, and deleting it is how a fleet goes dark silently.
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
  Check: NeedsConsoleOnboarding; Flags: postinstall shellexec skipifsilent

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
// ── Onboarding lives HERE, not in a console ──────────────────────────────────
//
// The "Set up Keld" page redeems a setup code (or runs the browser device flow
// with Atlas's approval page embedded in the wizard), then collects which AI
// tools to configure. CurStepChanged(ssPostInstall) does every destructive step
// afterwards.
//
// ⚠️ **THIS PAGE RENDERS; IT DECIDES NOTHING.** Every step runs `keld` and draws
// the NDJSON it emits (internal/cli's --json seam). Auth, tool detection and path
// resolution stay in Go, where they are tested. A Pascal reimplementation of any
// of them is the defect this design exists to avoid — and this file has a history
// of describing onboarding UI that was never built, so: if you are reading this
// and there is no page on screen, the code below is what is wrong, not the doc.
//
// ⚠️ **EVERYTHING INTERACTIVE HAPPENS BEFORE THE INSTALL, and Inno's structure
// forces it**: a CreateCustomPage page can only be placed ahead of wpReady, so a
// person can still cancel while it is on screen. It therefore does only what is
// safe to abandon — redeem a code (auth.json) and read which tools exist
// (--dry-run writes nothing). Rewriting a tool's settings.json and registering
// the logon task happen after the commit point.
//
// ⚠️ **NOTHING HERE BLOCKS, AND THAT IS NOT A STYLE CHOICE.** Pascal Script has
// no message-pump call — there is no AppProcessMessages — so a loop waiting on a
// device flow would freeze the wizard for minutes and Windows would mark it Not
// Responding. Every step is therefore started and left running, and a WinAPI
// SetTimer callback (the idiom Inno's own CodeDll.iss example uses) collects the
// results on the wizard's existing message loop.
//
// ⚠️ **THERE IS NO HANDOFF FILE, and that is not an oversight.** macOS needs one
// because its wizard pane and its postinstall script are different processes.
// Inno's [Code] is ONE process for the life of the wizard, so the variables this
// page sets are still here in CurStepChanged. Adding a handoff file would import
// the whole stale-file failure surface — a cancelled install leaving one behind,
// a version guard to detect that — for no benefit at all.

const
  RunNone         = 0;
  RunIdentity     = 1;
  RunLoginCode    = 2;
  RunLoginBrowser = 3;
  RunTools        = 4;
  RunClipboard    = 5;

  TickMs = 200;
  // Bound the work one tick may do: a burst of events must not turn a timer
  // callback into a visible stall.
  MaxEventsPerTick = 25;

function SetTimer(hWnd, nIDEvent, uElapse, lpTimerFunc: Longword): Longword;
  external 'SetTimer@user32.dll stdcall';
function KillTimer(hWnd, nIDEvent: Longword): BOOL;
  external 'KillTimer@user32.dll stdcall';

// The wizard's own pid, handed to every helper so a killed wizard — which writes
// no cancel sentinel — still reaps its children.
function GetCurrentProcessId: DWORD;
  external 'GetCurrentProcessId@kernel32.dll stdcall';

var
  SetupPage: TWizardPage;
  AccountHdr, StatusLbl, ToolsHdr, RestartLbl: TNewStaticText;
  CodeEdit: TNewEdit;
  ConnectBtn, RetryBtn: TNewButton;
  WebPanel: TPanel;
  ToolChecks: array of TNewCheckBox;
  ToolNames: array of String;

  Paired: Boolean;
  PairedAPIURL: String;
  IdentityChecked: Boolean;
  SignInStarted: Boolean;
  ApprovalShown: Boolean;
  PanelRunning: Boolean;

  TmpKeld, TmpHost, CancelFile, PanelStopFile: String;
  TimerID: Longword;
  RunCounter: Integer;

  // The one run in flight, if any.
  Mode: Integer;
  RunDir: String;
  RunSeqNo: Integer;

  PanelDir: String;
  PanelSeqNo: Integer;

  // Filled by the event handler, read when the run finishes.
  EvIdentityStatus, EvPrincipal, EvOrg, EvAPIURL, EvError: String;
  EvUserCode, EvApprovalURL, EvClipboard: String;
  PendingNames, PendingDisplays, PendingActions: TArrayOfString;

procedure StartTools; forward;
procedure BrowserSignIn; forward;
procedure CheckIdentity; forward;
procedure OnRunFinished(FinishedMode: Integer); forward;
procedure AfterClipboard; forward;

// ── Small helpers ────────────────────────────────────────────────────────────

// JsonStr pulls one string field out of a flat event object.
//
// ⚠️ Deliberately NOT a JSON parser. The events are flat objects written by Go,
// one per file, and every field this page reads is a string. Escaped quotes are
// skipped so a message containing one cannot truncate the value; \\ and \" are
// unescaped on the way out, which is every escape Go's encoder emits for these
// fields.
function JsonStr(const S, Key: String): String;
var
  Pat, Val: String;
  P, Q: Integer;
begin
  Result := '';
  Pat := '"' + Key + '":"';
  P := Pos(Pat, S);
  if P = 0 then
    exit;
  P := P + Length(Pat);
  Q := P;
  while Q <= Length(S) do
  begin
    if S[Q] = '\' then
      Q := Q + 2
    else if S[Q] = '"' then
      break
    else
      Q := Q + 1;
  end;
  Val := Copy(S, P, Q - P);
  StringChangeEx(Val, '\"', '"', True);
  StringChangeEx(Val, '\\', '\', True);
  Result := Val;
end;

function Pad4(N: Integer): String;
begin
  Result := IntToStr(N);
  while Length(Result) < 4 do
    Result := '0' + Result;
end;

procedure SetStatus(const S: String);
begin
  StatusLbl.Caption := S;
end;

function NewRunDir: String;
begin
  RunCounter := RunCounter + 1;
  Result := ExpandConstant('{tmp}\keldrun') + IntToStr(RunCounter);
  CreateDir(Result);
end;

function IsAlnum(C: Char): Boolean;
begin
  Result := ((C >= 'a') and (C <= 'z')) or ((C >= 'A') and (C <= 'Z')) or
            ((C >= '0') and (C <= '9'));
end;

// LooksLikePairingCode mirrors KeldLooksLikePairingCode in
// installers/macos/plugin/KeldCode.m. ⚠️ The two must agree: a code the Mac
// accepts and Windows rejects (or the reverse) is a difference nobody would
// think to look for. A single whitespace-free token whose last "/"-separated
// segment is two alphanumeric groups joined by one dash — that last rule is what
// separates a real code from ordinary copied text and from a plain download URL,
// whose final segment ("download") carries no separator.
function LooksLikePairingCode(S: String): Boolean;
var
  T, Seg, A, B: String;
  I, Dash: Integer;
begin
  Result := False;
  T := Trim(S);
  if (T = '') or (Length(T) > 128) then
    exit;
  for I := 1 to Length(T) do
    if (T[I] = ' ') or (T[I] = #9) or (T[I] = #13) or (T[I] = #10) then
      exit;

  Seg := T;
  for I := Length(T) downto 1 do
    if T[I] = '/' then
    begin
      Seg := Copy(T, I + 1, Length(T) - I);
      break;
    end;

  Dash := Pos('-', Seg);
  if Dash = 0 then
    exit;
  A := Copy(Seg, 1, Dash - 1);
  B := Copy(Seg, Dash + 1, Length(Seg) - Dash);
  if (A = '') or (B = '') or (Pos('-', B) > 0) then
    exit;
  for I := 1 to Length(A) do
    if not IsAlnum(A[I]) then
      exit;
  for I := 1 to Length(B) do
    if not IsAlnum(B[I]) then
      exit;
  Result := True;
end;

// ── Driving keld ─────────────────────────────────────────────────────────────

// StartRun launches one `keld` invocation and RETURNS IMMEDIATELY. The timer
// below collects its events.
function StartRun(NewMode: Integer; const Args: String): Boolean;
var
  Params: String;
  RC: Integer;
begin
  RunDir := NewRunDir;
  RunSeqNo := 1;
  EvError := '';
  Params := '--run "' + TmpKeld + '" --events-dir "' + RunDir + '"' +
            ' --sentinel "' + CancelFile + '"' +
            ' --parent-pid ' + IntToStr(GetCurrentProcessId) +
            ' -- ' + Args;
  Result := Exec(TmpHost, Params, '', SW_HIDE, ewNoWait, RC);
  if Result then
    Mode := NewMode
  else
    Mode := RunNone;
end;

// StartClipboardRead asks the helper what the clipboard holds. Same event
// plumbing as every other step, so there is one code path to get wrong.
function StartClipboardRead: Boolean;
var
  Params: String;
  RC: Integer;
begin
  RunDir := NewRunDir;
  RunSeqNo := 1;
  EvClipboard := '';
  Params := '--clipboard --events-dir "' + RunDir + '"';
  Result := Exec(TmpHost, Params, '', SW_HIDE, ewNoWait, RC);
  if Result then
    Mode := RunClipboard
  else
    Mode := RunNone;
end;

procedure HandleEvent(const Line: String);
var
  Ev: String;
  N: Integer;
begin
  Ev := JsonStr(Line, 'event');

  if Ev = 'error' then
  begin
    EvError := JsonStr(Line, 'message');
    exit;
  end;

  case Mode of
    RunIdentity:
      if Ev = 'identity' then
      begin
        EvIdentityStatus := JsonStr(Line, 'status');
        EvPrincipal := JsonStr(Line, 'principal');
        EvOrg := JsonStr(Line, 'org');
        EvAPIURL := JsonStr(Line, 'api_url');
      end;

    RunLoginCode, RunLoginBrowser:
      begin
        if Ev = 'device_code' then
        begin
          EvUserCode := JsonStr(Line, 'user_code');
          // ⚠️ Prefer Atlas's compact route. `verification_url` is the page built
          // for a real browser window and does not fit a wizard panel;
          // `installer_url` is the same approval sized for an embedded view. It
          // is ABSENT on an Atlas that predates it, and that case must still
          // work — hence the fallback rather than a requirement.
          EvApprovalURL := JsonStr(Line, 'installer_url');
          if EvApprovalURL = '' then
            EvApprovalURL := JsonStr(Line, 'verification_url');
        end
        else if Ev = 'authorized' then
        begin
          Paired := True;
          EvPrincipal := JsonStr(Line, 'principal');
          EvOrg := JsonStr(Line, 'org');
          PairedAPIURL := JsonStr(Line, 'api_url');
        end;
      end;

    RunClipboard:
      if Ev = 'clipboard' then
        EvClipboard := JsonStr(Line, 'text');

    RunTools:
      if Ev = 'tool' then
      begin
        N := GetArrayLength(PendingNames);
        SetArrayLength(PendingNames, N + 1);
        SetArrayLength(PendingDisplays, N + 1);
        SetArrayLength(PendingActions, N + 1);
        PendingNames[N] := JsonStr(Line, 'name');
        PendingDisplays[N] := JsonStr(Line, 'display');
        PendingActions[N] := JsonStr(Line, 'action');
      end;
  end;
end;

// ── The embedded approval panel ──────────────────────────────────────────────

// ⚠️ THE FIELDS ARE ATLAS'S, NOT OURS, AND THAT IS THE ENTIRE POINT. A native
// email/password form here would work, and would make this installer an auth
// client handling somebody's org password, dead-end the day Atlas gains SSO,
// break password-manager autofill, and teach people that typing credentials into
// an installer is normal — anyone can build a lookalike installer; only a real
// page can prove its own origin. Rendering Atlas's page costs none of that, and
// the wizard still never sends anyone to another app.
procedure StartPanel(const URL: String);
var
  Params: String;
  RC: Integer;
begin
  DeleteFile(PanelStopFile);
  PanelDir := NewRunDir;
  PanelSeqNo := 1;
  Params := '--panel ' + IntToStr(WebPanel.Handle) +
            ' --url "' + URL + '"' +
            ' --inset 1' +
            ' --events-dir "' + PanelDir + '"' +
            ' --sentinel "' + PanelStopFile + '"' +
            ' --parent-pid ' + IntToStr(GetCurrentProcessId);
  if Exec(TmpHost, Params, '', SW_HIDE, ewNoWait, RC) then
    PanelRunning := True;
end;

procedure ShowToolSection(Visible: Boolean);
var
  I: Integer;
begin
  ToolsHdr.Visible := Visible;
  for I := 0 to GetArrayLength(ToolChecks) - 1 do
    ToolChecks[I].Visible := Visible;
end;

// An Inno wizard page has a FIXED height, so the approval panel borrows the
// space of the sections below it rather than pushing them off the bottom.
procedure ShowApproval(const URL: String);
begin
  ApprovalShown := True;
  ShowToolSection(False);
  RestartLbl.Visible := False;
  CodeEdit.Visible := False;
  ConnectBtn.Visible := False;
  RetryBtn.Visible := False;
  WebPanel.Visible := True;
  StartPanel(URL);
end;

procedure HideApproval;
begin
  if PanelRunning then
  begin
    SaveStringToFile(PanelStopFile, 'stop', False);
    PanelRunning := False;
  end;
  ApprovalShown := False;
  WebPanel.Visible := False;
end;

// DrainPanel notices the one thing the page must react to: a machine with no
// WebView2 runtime, where the panel can never appear and the person would
// otherwise be left staring at an empty rectangle.
procedure DrainPanel;
var
  F, Status: String;
  Lines: TArrayOfString;
  RC: Integer;
begin
  F := AddBackslash(PanelDir) + Pad4(PanelSeqNo) + '.json';
  if not FileExists(F) then
    exit;
  PanelSeqNo := PanelSeqNo + 1;
  if not (LoadStringsFromFile(F, Lines) and (GetArrayLength(Lines) > 0)) then
    exit;
  if JsonStr(Lines[0], 'event') <> 'panel' then
    exit;
  Status := JsonStr(Lines[0], 'status');
  if Status = 'embedded' then
    exit;

  // Degraded, stated, and still no console: the approval opens in the default
  // browser and the device-flow poll carries on unchanged.
  //
  // ⚠️ DO NOT "FIX" THIS BY BUNDLING THE WEBVIEW2 EVERGREEN BOOTSTRAPPER. It is a
  // ~150 MB install-time download, for a case this fallback already covers, and
  // it needs admin rights this installer deliberately never asks for
  // (PrivilegesRequired=lowest).
  WebPanel.Visible := False;
  PanelRunning := False;
  SetStatus('Approve this device in the browser window that just opened.');
  if EvApprovalURL <> '' then
    ShellExec('open', EvApprovalURL, '', '', SW_SHOW, ewNoWait, RC);
end;

// DrainRun reads the in-flight run's event files in order.
//
// ⚠️ **A PUBLISHED FILE IS COMPLETE.** The helper writes <n>.tmp and renames it,
// so a file that exists can be read whole. That is why this reads numbered files
// rather than tailing one stream: a half-written line is a truncated JSON object
// this page would silently drop, and for `authorized` — the LAST event a login
// writes — dropping it means a paired machine the wizard believes is unpaired.
procedure DrainRun;
var
  F: String;
  Lines: TArrayOfString;
  Handled, FinishedMode: Integer;
begin
  Handled := 0;
  while Handled < MaxEventsPerTick do
  begin
    F := AddBackslash(RunDir) + Pad4(RunSeqNo) + '.json';
    if not FileExists(F) then
      exit;
    RunSeqNo := RunSeqNo + 1;
    Handled := Handled + 1;
    if LoadStringsFromFile(F, Lines) and (GetArrayLength(Lines) > 0) then
    begin
      if JsonStr(Lines[0], 'event') = '__exit' then
      begin
        FinishedMode := Mode;
        Mode := RunNone;
        OnRunFinished(FinishedMode);
        exit;
      end;
      HandleEvent(Lines[0]);
      // A device_code arrives MID-RUN and the panel must go up now, not when the
      // run finishes — the run does not finish until the person has approved.
      if (EvApprovalURL <> '') and not ApprovalShown then
      begin
        SetStatus('Sign in to approve this device.');
        ShowApproval(EvApprovalURL);
      end;
    end;
  end;
end;

// The wizard's own message loop dispatches this, so the window keeps painting
// and responding while a device flow runs for as long as it takes.
procedure TimerProc(H, Msg, Ev, Time: Longword);
begin
  if Mode <> RunNone then
    DrainRun;
  if PanelRunning then
    DrainPanel;
end;

// ── Steps ────────────────────────────────────────────────────────────────────

procedure RenderTools;
var
  I, N, Y: Integer;
  Title: String;
begin
  N := GetArrayLength(PendingNames);
  SetArrayLength(ToolChecks, N);
  SetArrayLength(ToolNames, N);
  Y := ScaleY(118);
  for I := 0 to N - 1 do
  begin
    ToolNames[I] := PendingNames[I];
    Title := PendingDisplays[I];
    if Title = '' then
      Title := PendingNames[I];
    // `action` is one of configured | already_configured | skipped_conflict |
    // will_configure. Everything except skipped_conflict is tickable and ticked
    // by default.
    if PendingActions[I] = 'skipped_conflict' then
      Title := Title + ' — has its own telemetry settings; leave it alone';
    ToolChecks[I] := TNewCheckBox.Create(SetupPage);
    ToolChecks[I].Parent := SetupPage.Surface;
    ToolChecks[I].SetBounds(0, Y, SetupPage.SurfaceWidth, ScaleY(17));
    ToolChecks[I].Caption := Title;
    ToolChecks[I].Checked := PendingActions[I] <> 'skipped_conflict';
    Y := Y + ScaleY(20);
  end;

  RestartLbl.Top := Y + ScaleY(4);
  if N = 0 then
    RestartLbl.Caption := 'No supported AI tools found on this device.'
  else
    // ⚠️ SAY THIS OUT LOUD. A tool reads its telemetry configuration ONCE, at
    // startup, so one already running keeps posting wherever it was pointed when
    // it launched — and nothing on the machine can detect or fix that from the
    // outside. Measured: a session started before setup ran emitted 0 telemetry
    // events over 11 hours while its blocks published normally.
    RestartLbl.Caption := 'Restart these apps after setup — they read their settings once, at startup.';
  RestartLbl.Visible := True;
  ToolsHdr.Visible := True;
end;

procedure StartTools;
begin
  SetArrayLength(PendingNames, 0);
  SetArrayLength(PendingDisplays, 0);
  SetArrayLength(PendingActions, 0);
  // ⚠️ Next is disabled until this finishes. A person who clicked through while
  // the list was still loading would reach ssPostInstall with no ticked tools,
  // and the empty-selection rule there would correctly configure nothing — for a
  // choice they never made.
  WizardForm.NextButton.Enabled := False;
  // --dry-run writes NOTHING: it enumerates what is installed and returns before
  // any write, which is what makes it safe to run before the commit point.
  if not StartRun(RunTools, 'signal setup --dry-run --json') then
    WizardForm.NextButton.Enabled := True;
end;

procedure MarkConnected;
begin
  Paired := True;
  CodeEdit.Visible := False;
  ConnectBtn.Visible := False;
  RetryBtn.Visible := False;
  SetStatus('Connected — ' + EvPrincipal + ' · ' + EvOrg);
  WizardForm.NextButton.Enabled := True;
end;

procedure ConnectWithCode(const Code: String);
begin
  ConnectBtn.Enabled := False;
  SetStatus('Connecting…');
  if not StartRun(RunLoginCode, 'login --code "' + Code + '" --json') then
  begin
    ConnectBtn.Enabled := True;
    SetStatus('Could not start sign-in. Try again.');
  end;
end;

procedure ConnectClick(Sender: TObject);
var
  Code: String;
begin
  if Mode <> RunNone then
    exit;
  Code := Trim(CodeEdit.Text);
  if Code = '' then
  begin
    SetStatus('Enter the setup code from your Keld download page.');
    exit;
  end;
  ConnectWithCode(Code);
end;

// BrowserSignIn runs the OAuth device-authorization flow with Atlas's approval
// page embedded in the panel.
//
// ⚠️ `--no-browser` is LOAD-BEARING: without it the CLI opens the verification
// URL itself, putting the same approval in two places at once — one of them the
// browser this page exists to avoid sending anyone to.
//
// ⚠️ **THE USER CODE IS DELIBERATELY NOT SHOWN, AND THIS COMMENT USED TO ARGUE
// THE OPPOSITE.** The rule it invoked is real — device flow's protection against
// being phished into approving someone else's sign-in is that the code on screen
// matches the code on the page being approved — but it does not reach this flow.
// Atlas's embedded route reads the code from its own URL and posts it
// (services/web/app/cli/installer/page.tsx); it never renders it. So there was
// nothing on screen to compare against, and displaying a code beside a page that
// does not show one asks the person to verify a match they cannot perform. A
// check nobody can carry out is worse than no check: it looks like one.
//
// The protection that DOES apply here is that the code never left this process —
// the page fetched it and put it in the URL itself, with no human in the loop to
// mistype or be redirected.
procedure BrowserSignIn;
begin
  if SignInStarted then
    exit;
  SignInStarted := True;
  SetStatus('Signing in to Keld…');
  RetryBtn.Visible := False;
  EvUserCode := '';
  EvApprovalURL := '';
  if not StartRun(RunLoginBrowser, 'login --json --no-browser') then
  begin
    SignInStarted := False;
    SetStatus('Could not start sign-in. Try again, or paste a setup code.');
    RetryBtn.Visible := True;
  end;
end;

// CheckIdentity asks whether this machine is ALREADY connected, and asks ATLAS
// rather than looking for auth.json.
//
// ⚠️ `keld whoami` on its own never contacts Atlas — it prints what a local file
// says — so a revoked token and a live one are indistinguishable to it. Enabling
// Next on that basis would let someone install with a dead credential and
// collect nothing, which is the confused state this page exists to prevent.
// `--verify` performs the same Onboarding() call ssPostInstall will make minutes
// later, so a verified answer predicts that step rather than merely correlating
// with it.
procedure CheckIdentity;
begin
  // ⚠️ EXTRACT BEFORE FIRST USE. A `dontcopy` file is stored INSIDE setup.exe and
  // is not on disk until ExtractTemporaryFile puts it there; without this the
  // page would drive two paths that do not exist and every run would fail to
  // start. Both are needed: the CLI, and the helper that runs it.
  if TmpKeld = '' then
  begin
    ExtractTemporaryFile('keld.exe');
    ExtractTemporaryFile('keld-wizard-host.exe');
    TmpKeld := ExpandConstant('{tmp}\keld.exe');
    TmpHost := ExpandConstant('{tmp}\keld-wizard-host.exe');
  end;

  SetStatus('Checking this PC…');
  RetryBtn.Visible := False;
  EvIdentityStatus := '';
  if not StartRun(RunIdentity, 'whoami --verify --json') then
  begin
    SetStatus('Could not check this PC. Try again.');
    RetryBtn.Visible := True;
  end;
end;

procedure RetryClick(Sender: TObject);
begin
  if Mode <> RunNone then
    exit;
  SignInStarted := False;
  CheckIdentity;
end;

procedure AfterIdentity;
begin
  if EvIdentityStatus = 'verified' then
  begin
    PairedAPIURL := EvAPIURL;
    MarkConnected;
    StartTools;
    exit;
  end;

  if EvIdentityStatus = 'unreachable' then
  begin
    // ⚠️ THE THREE FAILURE STATES ARE NOT INTERCHANGEABLE. This one means we
    // learned NOTHING about the credential, so the install must not proceed —
    // there is deliberately no button that continues unverified.
    SetStatus('Can''t reach Atlas — Keld can''t be set up right now.');
    RetryBtn.Visible := True;
    WizardForm.NextButton.Enabled := False;
    exit;
  end;

  // `none` or `unauthorized`: this machine needs connecting. An expired
  // credential is not a reason to nag about the old one, so both read the same.
  //
  // The clipboard is tried first only because it is INSTANT for someone who came
  // straight from the Keld download page and clicked Copy; with nothing there,
  // the page fetches a code itself rather than asking anyone to go and find one.
  //
  // ⚠️ The READ goes through the helper because Pascal Script has no clipboard
  // function at all — see clipboard_windows.go. The JUDGEMENT stays here, where
  // it mirrors macOS.
  if not StartClipboardRead then
    BrowserSignIn;
end;

procedure AfterClipboard;
var
  Pasted: String;
begin
  Pasted := Trim(EvClipboard);
  if LooksLikePairingCode(Pasted) then
  begin
    CodeEdit.Text := Pasted;
    ConnectWithCode(Pasted);
    exit;
  end;
  BrowserSignIn;
end;

procedure AfterLogin(WasBrowser: Boolean);
begin
  if WasBrowser then
    HideApproval;
  ConnectBtn.Enabled := True;

  if Paired then
  begin
    MarkConnected;
    StartTools;
    exit;
  end;

  if WasBrowser then
  begin
    // All-or-nothing: Next stays disabled and the only ways on are to try again
    // or to paste a code by hand.
    SignInStarted := False;
    CodeEdit.Visible := True;
    ConnectBtn.Visible := True;
    if EvError <> '' then
      SetStatus('Sign-in didn''t finish (' + EvError + '). Try again, or paste a setup code.')
    else
      SetStatus('Sign-in didn''t finish. Try again, or paste a setup code.');
    RetryBtn.Visible := True;
  end
  else
  begin
    if EvError <> '' then
      SetStatus(EvError)
    else
      SetStatus('That code was not accepted. Check it and try again.');
  end;
end;

procedure OnRunFinished(FinishedMode: Integer);
begin
  case FinishedMode of
    RunIdentity:
      AfterIdentity;
    RunLoginCode:
      AfterLogin(False);
    RunLoginBrowser:
      AfterLogin(True);
    RunClipboard:
      AfterClipboard;
    RunTools:
      begin
        RenderTools;
        WizardForm.NextButton.Enabled := Paired;
      end;
  end;
end;

// ── Wizard plumbing ──────────────────────────────────────────────────────────

procedure InitializeWizard();
var
  W: Integer;
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

  CancelFile := ExpandConstant('{tmp}\keld-wizard-cancel');
  PanelStopFile := ExpandConstant('{tmp}\keld-wizard-panel-stop');

  SetupPage := CreateCustomPage(wpInfoBefore, 'Set up Keld',
    'Sign this device in to your Keld account.');
  W := SetupPage.SurfaceWidth;

  AccountHdr := TNewStaticText.Create(SetupPage);
  AccountHdr.Parent := SetupPage.Surface;
  AccountHdr.SetBounds(0, 0, W, ScaleY(15));
  AccountHdr.Font.Style := [fsBold];
  AccountHdr.Caption := 'Your Keld account';

  ConnectBtn := TNewButton.Create(SetupPage);
  ConnectBtn.Parent := SetupPage.Surface;
  ConnectBtn.SetBounds(W - ScaleX(90), ScaleY(20), ScaleX(90), ScaleY(23));
  ConnectBtn.Caption := 'Connect';
  ConnectBtn.OnClick := @ConnectClick;

  CodeEdit := TNewEdit.Create(SetupPage);
  CodeEdit.Parent := SetupPage.Surface;
  CodeEdit.SetBounds(0, ScaleY(20), W - ScaleX(98), ScaleY(23));
  CodeEdit.Text := '';

  StatusLbl := TNewStaticText.Create(SetupPage);
  StatusLbl.Parent := SetupPage.Surface;
  StatusLbl.SetBounds(0, ScaleY(50), W, ScaleY(32));
  StatusLbl.WordWrap := True;
  StatusLbl.Caption := 'Checking this PC…';

  RetryBtn := TNewButton.Create(SetupPage);
  RetryBtn.Parent := SetupPage.Surface;
  RetryBtn.SetBounds(0, ScaleY(84), ScaleX(90), ScaleY(23));
  RetryBtn.Caption := 'Try again';
  RetryBtn.OnClick := @RetryClick;
  RetryBtn.Visible := False;

  WebPanel := TPanel.Create(SetupPage);
  WebPanel.Parent := SetupPage.Surface;
  WebPanel.SetBounds(0, ScaleY(70), W, SetupPage.SurfaceHeight - ScaleY(74));
  WebPanel.BevelOuter := bvNone;
  WebPanel.Caption := '';
  // A dark-gray hairline around the embedded page. It IS the panel showing
  // through: the helper cannot paint on a window this process owns, so it insets
  // its webview by one pixel (--inset, below) and this colour fills the ring.
  // A panel colour that fails to apply costs the border and nothing else.
  WebPanel.Color := $00595959;
  WebPanel.Visible := False;

  ToolsHdr := TNewStaticText.Create(SetupPage);
  ToolsHdr.Parent := SetupPage.Surface;
  ToolsHdr.SetBounds(0, ScaleY(98), W, ScaleY(15));
  ToolsHdr.Font.Style := [fsBold];
  ToolsHdr.Caption := 'Your AI tools';
  ToolsHdr.Visible := False;

  RestartLbl := TNewStaticText.Create(SetupPage);
  RestartLbl.Parent := SetupPage.Surface;
  RestartLbl.SetBounds(0, ScaleY(200), W, ScaleY(30));
  RestartLbl.WordWrap := True;
  RestartLbl.Visible := False;

  TimerID := SetTimer(0, 0, TickMs, CreateCallback(@TimerProc));
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if CurPageID <> SetupPage.ID then
    exit;

  // ⚠️ RE-ASSERT THE GATE ON EVERY ENTRY. Inno resets NextButton.Enabled when it
  // changes page, so a gate set once leaks open the moment someone goes Back and
  // forward again — and this page's whole job is that a machine cannot finish the
  // install unconnected.
  WizardForm.NextButton.Enabled := Paired and (Mode = RunNone);

  if not IdentityChecked then
  begin
    IdentityChecked := True;
    CheckIdentity;
  end;
end;

// The console fallback runs only when this page did not complete a pairing.
//
// ⚠️ NARROWER THAN macOS'S CONDITION, AND DELIBERATELY SO. The pkg gates on BOTH
// "no handoff file" AND "no hook.json", because its wizard pane can fail to load
// with no diagnostic at all and because "Set up later" is a reachable choice
// there. Neither applies here: there is no separate process to fail to load, and
// there is no "Set up later". So the question is simply whether this page paired.
function NeedsConsoleOnboarding: Boolean;
begin
  Result := not Paired;
end;

procedure CurStepChanged(CurStep: TSetupStep);
var
  Args: String;
  I, RC: Integer;
  AnyTicked: Boolean;
begin
  if CurStep <> ssPostInstall then
    exit;
  if not Paired then
    exit;

  // ⚠️ AN EMPTY SELECTION MUST CONFIGURE NOTHING, NOT EVERYTHING.
  // `tools.Select(nil)` reads "no --tool flags at all" as "configure every
  // detected adapter". That is right when the page never ran, and wrong when the
  // page DID run and every checkbox was unticked — that person made a choice.
  AnyTicked := False;
  for I := 0 to GetArrayLength(ToolChecks) - 1 do
    if ToolChecks[I].Checked then
      AnyTicked := True;
  if not AnyTicked then
    exit;

  // ⚠️ --bin-path PINS THE INSTALLED BINARY. The page drove {tmp}\keld.exe, a
  // path that stops existing when the wizard closes; without this flag every
  // hook written here points at it and silently never runs.
  Args := 'signal setup --yes --bin-path "' + ExpandConstant('{app}\keld.exe') + '"';
  if PairedAPIURL <> '' then
    Args := Args + ' --api-url "' + PairedAPIURL + '"';
  for I := 0 to GetArrayLength(ToolChecks) - 1 do
    if ToolChecks[I].Checked then
      Args := Args + ' --tool ' + ToolNames[I];

  Exec(ExpandConstant('{app}\keld.exe'), Args, '', SW_HIDE, ewWaitUntilTerminated, RC);
end;

// Cancel and teardown both write the sentinel, so a helper — and the `keld`
// child inside its job object — cannot outlive the wizard. A device-flow login
// polls Atlas until the code expires, so without this a cancelled install leaves
// a process polling in the background with nothing to report to.
procedure CancelButtonClick(CurPageID: Integer; var Cancel, Confirm: Boolean);
begin
  SaveStringToFile(CancelFile, 'cancel', False);
  SaveStringToFile(PanelStopFile, 'stop', False);
end;

procedure DeinitializeSetup();
begin
  if TimerID <> 0 then
    KillTimer(0, TimerID);
  SaveStringToFile(CancelFile, 'cancel', False);
  SaveStringToFile(PanelStopFile, 'stop', False);
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
