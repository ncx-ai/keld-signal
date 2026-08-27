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
; TWO ENTRIES, AND BOTH ARE LOAD-BEARING. The first ALWAYS runs; the second is the
; human's onboarding and may legitimately not run at all.
;
; 1. REGISTER THE AGENT UNCONDITIONALLY. With no console attached, keld-agent
;    install takes its headless branch: it writes the v2 agent-config.json,
;    registers the logon task, starts the daemon, and prompts for nothing. The
;    daemon then IDLES on awaitConfig until someone completes setup, which is a
;    documented, supported state — not a crash.
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
Filename: "{app}\keld-agent.exe"; Parameters: "install"; \
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

[Code]
function NeedsAddPath(P: string): Boolean;
var
  O: string;
begin
  if not RegQueryStringValue(HKCU, 'Environment', 'Path', O) then
    O := '';
  Result := Pos(';' + P + ';', ';' + O + ';') = 0;
end;
