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

[Tasks]
Name: "addtopath"; Description: "Add Keld to my PATH"; Flags: checkedonce

[Registry]
Root: HKCU; Subkey: "Environment"; ValueType: expandsz; ValueName: "Path"; \
  ValueData: "{olddata};{app}"; Tasks: addtopath; Check: NeedsAddPath('{app}')

[Run]
; Onboarding runs in a VISIBLE console in the user's session and registers the
; scheduled task itself (keld-agent install does login -> signal setup -> service,
; in that order, and writes the v2 agent-config.json before any of it).
;
; This used to be `keld-agent.exe install` with `runhidden nowait`, which meant the
; interactive login ran where nobody could see or complete it, on a step Inno did
; not wait for and could not report. Every Windows machine registered its logon
; task and then idled on awaitConfig forever, collecting nothing and saying
; nothing. DO NOT RE-ADD runhidden HERE.
;
; skipifsilent matters: a /SILENT install (an MDM push) must not block on a console
; waiting for a human to paste a code. Such a machine is finished by
; `keld-agent install --code <CODE>` from the management tool instead.
Filename: "{app}\onboard.cmd"; Description: "Set up Keld"; \
  Flags: postinstall shellexec skipifsilent

[Code]
function NeedsAddPath(P: string): Boolean;
var
  O: string;
begin
  if not RegQueryStringValue(HKCU, 'Environment', 'Path', O) then
    O := '';
  Result := Pos(';' + P + ';', ';' + O + ';') = 0;
end;
