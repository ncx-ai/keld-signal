# Windows installer onboarding wizard page — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Embed the device-auth + tool-setup flow in `keld-setup.exe` as a custom Inno wizard page that drives `keld login --json` / `keld signal setup --json`.

**Architecture:** A post-install custom page (`CreateCustomPage(wpInstalling, …)`) runs a small state machine driven by a WinAPI timer (`SetTimer` + `CreateCallback`). It launches `keld … --json` asynchronously (redirecting NDJSON to a temp file), polls the file each tick, parses events with tiny `Pos`/`Copy` helpers, and updates the page (live code, browser button, per-tool progress). Everything lives in the existing `.iss` `[Code]` — no new binary or toolchain.

**Tech Stack:** Inno Setup 6 (Pascal Script), the shipped `keld --json` interface. Built by `iscc` on the `windows-latest` CI runner.

## Global Constraints

- **No Inno/`iscc` on the dev box (Linux).** The `.iss` is compiled only in CI; the UX is human-verified on Windows. Do not claim the Windows UX works from this environment — only that the script is structurally sound and compiles in CI.
- Do not change the `keld --json` contract (shipped).
- Keep the existing `[Setup]/[Files]/[Tasks]/[Registry]/[Run]` and the `NeedsAddPath` function intact; the `[Run] keld-agent install` stays (headless ⇒ TTY guard registers the service only, independent of the page).
- Best-effort: the page never blocks the install; Next is always enabled (proceeding early = skip); failures show a manual-fallback hint.
- End commit messages with `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.

---

### Task 1: Rewrite `keld-agent.iss` with the onboarding page

**Files:**
- Modify (replace whole file): `installers/windows/keld-agent.iss`

**Interfaces:**
- Consumes: `keld.exe login --json --no-browser` and `keld.exe signal setup --json` (installed to `{app}`).
- Produces: the `keld-setup.exe` wizard behavior (a "Set up Keld" page after install).

- [ ] **Step 1: Replace the file with the full script**

```pascal
; Inno Setup script — build in CI: iscc installers\windows\keld-agent.iss
; Per-user install (no admin). Files staged next to this script by CI:
;   keld.exe, keld-agent.exe, keld-agent-sidecar\  (frozen one-dir)
; KELD_VERSION is set in the environment by CI.
#define MyVersion GetEnv("KELD_VERSION")

[Setup]
AppName=Keld
AppVersion={#MyVersion}
DefaultDirName={localappdata}\Programs\keld
PrivilegesRequired=lowest
DisableProgramGroupPage=yes
ChangesEnvironment=yes
OutputBaseFilename=keld-setup

[Files]
Source: "keld.exe";             DestDir: "{app}"; Flags: ignoreversion
Source: "keld-agent.exe";       DestDir: "{app}"; Flags: ignoreversion
Source: "keld-agent-sidecar\*"; DestDir: "{app}"; Flags: ignoreversion recursesubdirs createallsubdirs

[Tasks]
Name: "addtopath"; Description: "Add Keld to my PATH"; Flags: checkedonce

[Registry]
Root: HKCU; Subkey: "Environment"; ValueType: expandsz; ValueName: "Path"; \
  ValueData: "{olddata};{app}"; Tasks: addtopath; Check: NeedsAddPath('{app}')

[Run]
; Register the per-user logon task (keld-agent install uses schtasks on Windows).
; runhidden ⇒ no TTY ⇒ the shipped guard registers the service only; the onboarding
; page below drives login + setup via the --json interface.
Filename: "{app}\keld-agent.exe"; Parameters: "install"; Flags: runhidden nowait postinstall

[Code]
const
  TIMER_MS   = 700;
  MAX_TICKS  = 430;  { ~5 min at 700ms }

function SetTimer(hWnd, nIDEvent, uElapse, lpTimerFunc: LongWord): LongWord;
  external 'SetTimer@user32.dll stdcall';
function KillTimer(hWnd, nIDEvent: LongWord): LongWord;
  external 'KillTimer@user32.dll stdcall';

var
  OnboardPage: TWizardPage;
  StatusLabel: TNewStaticText;
  CodeLabel:   TNewStaticText;
  OpenBtn:     TNewButton;
  RetryBtn:    TNewButton;
  ProgressMemo: TNewMemo;
  TimerID:   LongWord;
  Phase:     Integer;   { 0=login  1=setup  2=done  3=failed }
  LinesSeen: Integer;
  Ticks:     Integer;
  VerifyURL: String;
  LoginFile: String;
  SetupFile: String;

function NeedsAddPath(P: string): Boolean;
var
  O: string;
begin
  if not RegQueryStringValue(HKCU, 'Environment', 'Path', O) then
    O := '';
  Result := Pos(';' + P + ';', ';' + O + ';') = 0;
end;

{ --- tiny NDJSON field extractors (output is controlled, no JSON lib needed) --- }
function GetJsonStr(Line, Key: String): String;
var
  Marker, Rest: String;
  P, QP: Integer;
begin
  Result := '';
  Marker := '"' + Key + '":"';
  P := Pos(Marker, Line);
  if P = 0 then exit;
  Rest := Copy(Line, P + Length(Marker), Length(Line));
  QP := Pos('"', Rest);
  if QP = 0 then exit;
  Result := Copy(Rest, 1, QP - 1);
end;

function GetEventKind(Line: String): String;
begin
  Result := GetJsonStr(Line, 'event');
end;

procedure StopTimer();
begin
  if TimerID <> 0 then begin
    KillTimer(0, TimerID);
    TimerID := 0;
  end;
end;

procedure Fail(Msg: String);
begin
  StopTimer();
  Phase := 3;
  StatusLabel.Caption := Msg + ' You can finish later by running "keld login" then '
    + '"keld signal setup". Click Next to continue.';
  OpenBtn.Enabled := False;
  RetryBtn.Enabled := True;
end;

procedure Succeed();
begin
  StopTimer();
  Phase := 2;
  StatusLabel.Caption := 'You''re all set. Click Next to finish.';
  OpenBtn.Enabled := False;
  RetryBtn.Enabled := False;
end;

procedure StartLogin();
var
  ec: Integer;
begin
  Phase := 0;
  LinesSeen := 0;
  VerifyURL := '';
  DeleteFile(LoginFile);
  CodeLabel.Caption := '';
  OpenBtn.Enabled := False;
  StatusLabel.Caption := 'Starting sign-in…';
  ShellExec('', ExpandConstant('{cmd}'),
    '/C ""' + ExpandConstant('{app}\keld.exe') + '" login --json --no-browser > "' + LoginFile + '""',
    '', SW_HIDE, ewNoWait, ec);
end;

procedure StartSetup();
var
  ec: Integer;
begin
  Phase := 1;
  LinesSeen := 0;
  DeleteFile(SetupFile);
  OpenBtn.Enabled := False;
  StatusLabel.Caption := 'Signed in. Configuring your tools…';
  ShellExec('', ExpandConstant('{cmd}'),
    '/C ""' + ExpandConstant('{app}\keld.exe') + '" signal setup --json > "' + SetupFile + '""',
    '', SW_HIDE, ewNoWait, ec);
end;

procedure TimerTick(H, Msg, Event, Time: LongWord);
var
  Lines: TArrayOfString;
  n: Integer;
  Line, Kind: String;
begin
  Ticks := Ticks + 1;
  if Ticks > MAX_TICKS then begin
    Fail('Timed out.');
    exit;
  end;

  if Phase = 0 then Line := LoginFile
  else if Phase = 1 then Line := SetupFile
  else exit;

  if not LoadStringsFromFile(Line, Lines) then exit;
  n := GetArrayLength(Lines);
  while LinesSeen < n do begin
    Line := Lines[LinesSeen];
    Kind := GetEventKind(Line);
    { A blank kind on the final element likely means a partial line still being
      written — wait for the next tick rather than skipping it. }
    if (Kind = '') and (LinesSeen = n - 1) then break;
    LinesSeen := LinesSeen + 1;

    if Kind = 'device_code' then begin
      VerifyURL := GetJsonStr(Line, 'verification_url');
      CodeLabel.Caption := GetJsonStr(Line, 'user_code');
      StatusLabel.Caption := 'Approve in your browser, then wait…';
      OpenBtn.Enabled := True;
    end else if Kind = 'tool' then begin
      ProgressMemo.Lines.Add('• ' + GetJsonStr(Line, 'display') + ' — ' + GetJsonStr(Line, 'action'));
    end else if Kind = 'authorized' then begin
      if Phase = 0 then begin StartSetup(); exit; end;
    end else if Kind = 'done' then begin
      Succeed();
      exit;
    end else if Kind = 'error' then begin
      Fail('Error: ' + GetJsonStr(Line, 'message'));
      exit;
    end;
  end;
end;

procedure OpenBtnClick(Sender: TObject);
var
  ec: Integer;
begin
  if VerifyURL <> '' then
    ShellExec('open', VerifyURL, '', '', SW_SHOWNORMAL, ewNoWait, ec);
end;

procedure RetryBtnClick(Sender: TObject);
begin
  RetryBtn.Enabled := False;
  ProgressMemo.Lines.Clear;
  Ticks := 0;
  StartLogin();
  if TimerID = 0 then
    TimerID := SetTimer(0, 0, TIMER_MS, CreateCallback(@TimerTick));
end;

procedure InitializeWizard();
begin
  LoginFile := ExpandConstant('{tmp}\keld_login.ndjson');
  SetupFile := ExpandConstant('{tmp}\keld_setup.ndjson');

  OnboardPage := CreateCustomPage(wpInstalling, 'Set up Keld',
    'Sign in and configure your AI coding tools.');

  StatusLabel := TNewStaticText.Create(WizardForm);
  StatusLabel.Parent := OnboardPage.Surface;
  StatusLabel.Left := 0;
  StatusLabel.Top := 0;
  StatusLabel.Width := OnboardPage.SurfaceWidth;
  StatusLabel.AutoSize := False;
  StatusLabel.WordWrap := True;
  StatusLabel.Height := ScaleY(34);
  StatusLabel.Caption := 'Preparing…';

  CodeLabel := TNewStaticText.Create(WizardForm);
  CodeLabel.Parent := OnboardPage.Surface;
  CodeLabel.Left := 0;
  CodeLabel.Top := ScaleY(40);
  CodeLabel.Font.Size := 20;
  CodeLabel.Font.Style := [fsBold];
  CodeLabel.Caption := '';

  OpenBtn := TNewButton.Create(WizardForm);
  OpenBtn.Parent := OnboardPage.Surface;
  OpenBtn.Left := 0;
  OpenBtn.Top := ScaleY(76);
  OpenBtn.Width := ScaleX(180);
  OpenBtn.Height := ScaleY(24);
  OpenBtn.Caption := 'Open browser to approve';
  OpenBtn.Enabled := False;
  OpenBtn.OnClick := @OpenBtnClick;

  RetryBtn := TNewButton.Create(WizardForm);
  RetryBtn.Parent := OnboardPage.Surface;
  RetryBtn.Left := ScaleX(190);
  RetryBtn.Top := ScaleY(76);
  RetryBtn.Width := ScaleX(90);
  RetryBtn.Height := ScaleY(24);
  RetryBtn.Caption := 'Retry';
  RetryBtn.Enabled := False;
  RetryBtn.OnClick := @RetryBtnClick;

  ProgressMemo := TNewMemo.Create(WizardForm);
  ProgressMemo.Parent := OnboardPage.Surface;
  ProgressMemo.Left := 0;
  ProgressMemo.Top := ScaleY(110);
  ProgressMemo.Width := OnboardPage.SurfaceWidth;
  ProgressMemo.Height := ScaleY(110);
  ProgressMemo.ReadOnly := True;
  ProgressMemo.ScrollBars := ssVertical;
end;

procedure CurPageChanged(CurPageID: Integer);
begin
  if (OnboardPage <> nil) and (CurPageID = OnboardPage.ID) then begin
    Ticks := 0;
    StartLogin();
    if TimerID = 0 then
      TimerID := SetTimer(0, 0, TIMER_MS, CreateCallback(@TimerTick));
  end else begin
    StopTimer();
  end;
end;

procedure DeinitializeSetup();
begin
  StopTimer();
end;
```

- [ ] **Step 2: Local structural checks (no `iscc` here)**

Run:
```bash
cd installers/windows
python3 - <<'PY'
import re
s=open('keld-agent.iss').read()
for sec in ['[Setup]','[Files]','[Run]','[Registry]','[Code]']:
    assert sec in s, f"missing {sec}"
# every referenced proc/var is defined somewhere in the file
for ident in ['NeedsAddPath','GetJsonStr','GetEventKind','StopTimer','Fail','Succeed',
              'StartLogin','StartSetup','TimerTick','OpenBtnClick','RetryBtnClick',
              'InitializeWizard','CurPageChanged','DeinitializeSetup',
              'OnboardPage','StatusLabel','CodeLabel','OpenBtn','RetryBtn','ProgressMemo',
              'LoginFile','SetupFile','TimerID','LinesSeen','VerifyURL']:
    assert s.count(ident) >= 2, f"identifier {ident} referenced but not defined?"
# balance: begin vs end (allow end; and end.) — rough sanity, not a compiler
begins=len(re.findall(r'\bbegin\b', s))
ends=len(re.findall(r'\bend\b', s))
print(f"begin={begins} end={ends}")
assert ends>=begins, "unbalanced begin/end (more begins than ends)"
print("structure OK")
PY```
Expected: `structure OK` (and a `begin=/end=` line).

- [ ] **Step 3: Commit**

```bash
git add installers/windows/keld-agent.iss
git commit -m "feat(windows): onboarding wizard page driving keld --json

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: Docs — record the Windows onboarding page

**Files:**
- Modify: `README.md` (Windows install section)
- Modify: `AGENTS.md` (installers note)

- [ ] **Step 1: README — note the wizard page**

In `README.md`, in the "### Windows — `keld-setup.exe`" section, after the install
sentence, add:

```markdown

During install a **Set up Keld** step walks you through sign-in and tool
configuration (it drives `keld login` / `keld signal setup`). You can click Next to
skip it and run those two commands yourself later — the background agent is
registered either way.
```

- [ ] **Step 2: AGENTS.md — extend the installer note**

In `AGENTS.md`, alongside the macOS onboarding gotcha, add:

```markdown
- **Windows onboarding UI:** `installers/windows/keld-agent.iss` `[Code]` adds a
  post-install Inno wizard page that drives the `keld --json` interface (timer +
  async NDJSON temp-file polling). Compiled by `iscc` on the Windows CI runner;
  UX is human-verified on Windows.
```

- [ ] **Step 3: Commit**

```bash
git add README.md AGENTS.md
git commit -m "docs: Windows installer onboarding page

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:**
- Post-install custom page (`CreateCustomPage(wpInstalling,…)`) → Task 1 `InitializeWizard`.
- Timer-driven state machine (`SetTimer`+`CreateCallback`) → Task 1 `TimerTick`/`CurPageChanged`.
- Async launch + temp-file NDJSON polling → `StartLogin`/`StartSetup`/`TimerTick`.
- Live code + Open browser + per-tool progress + retry → controls + handlers.
- Partial-last-line guard, unknown-event tolerance, phase-guarded transitions → `TimerTick`.
- Best-effort (Next always enabled, manual fallback, timer always killed) → `Fail`/`Succeed`/`DeinitializeSetup`.
- Service still registered via headless `[Run] keld-agent install` → `[Run]` unchanged.
- Existing sections + `NeedsAddPath` preserved → Task 1 file.
- Docs → Task 2.
- Verification constraint (no local iscc; CI compile; human smoke) → honored; only structural checks claimed locally.

**Placeholder scan:** none — the full `.iss` is given verbatim.

**Type/identifier consistency:** every proc/var used in the `[Code]` block
(`OnboardPage`, `StatusLabel`, `CodeLabel`, `OpenBtn`, `RetryBtn`, `ProgressMemo`,
`TimerID`, `LinesSeen`, `Ticks`, `VerifyURL`, `LoginFile`, `SetupFile`,
`GetJsonStr`, `GetEventKind`, `StopTimer`, `Fail`, `Succeed`, `StartLogin`,
`StartSetup`, `TimerTick`) is declared in the same file; each helper is defined
before its first use (Pascal requires this — order: externals → vars → NeedsAddPath
→ GetJsonStr → GetEventKind → StopTimer → Fail → Succeed → StartLogin → StartSetup
→ TimerTick → OpenBtnClick → RetryBtnClick → InitializeWizard → CurPageChanged →
DeinitializeSetup).

**Known blind-authoring risks (flagged, not resolvable here):**
- `SetTimer(0,…)` + `CreateCallback(@TimerTick)`: the WM_TIMER callback firing on
  the wizard's message loop, and the `CreateCallback`/stdcall thunk, can only be
  confirmed at runtime on Windows.
- `cmd /C` quoting of the redirected command; exact behavior of `LoadStringsFromFile`
  on a file being appended concurrently.
- CI (`iscc`) catches compile errors; runtime behavior needs the human smoke test.
