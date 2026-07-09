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
; runhidden => no TTY => the shipped guard registers the service only; the onboarding
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
  StatusLabel.Caption := 'Starting sign-in...';
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
  StatusLabel.Caption := 'Signed in. Configuring your tools...';
  ShellExec('', ExpandConstant('{cmd}'),
    '/C ""' + ExpandConstant('{app}\keld.exe') + '" signal setup --json > "' + SetupFile + '""',
    '', SW_HIDE, ewNoWait, ec);
end;

procedure TimerTick(H, Msg, Event, Time: LongWord);
var
  Lines: TArrayOfString;
  n: Integer;
  Fname, Line, Kind: String;
begin
  Ticks := Ticks + 1;
  if Ticks > MAX_TICKS then begin
    Fail('Timed out.');
    exit;
  end;

  if Phase = 0 then Fname := LoginFile
  else if Phase = 1 then Fname := SetupFile
  else exit;

  if not LoadStringsFromFile(Fname, Lines) then exit;
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
      StatusLabel.Caption := 'Approve in your browser, then wait...';
      OpenBtn.Enabled := True;
    end else if Kind = 'tool' then begin
      ProgressMemo.Lines.Add('- ' + GetJsonStr(Line, 'display') + ' - ' + GetJsonStr(Line, 'action'));
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
  StatusLabel.Caption := 'Preparing...';

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
