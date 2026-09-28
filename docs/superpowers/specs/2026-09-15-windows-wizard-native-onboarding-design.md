# Windows wizard-native onboarding — eliminating the console from the installer

**Status:** design, 2026-09-15. Implements on Windows what
`2026-09-14-macos-wizard-native-onboarding-design.md` implemented on macOS: no
console, no browser hand-off, no second app — the whole install and setup
happens inside the Inno Setup wizard.
**Scope:** the Windows installer only. macOS is done and unchanged; Linux
(`install.sh`) is out of scope and unaffected.

## 1. The problem

`installers/windows/keld-agent.iss` ends with a `[Run]` entry that opens
**`onboard.cmd`** in a visible console, which is where onboarding happens:

```
set /p CODE="Paste your setup code from the Keld download page (or press Enter to log in with a browser): "
```

That console is not cosmetic. Entry 1 of `[Run]` registers the agent
`--headless`, and a headless registration deliberately prompts for nothing — so
without the console the machine installs, registers its logon task, and idles on
`awaitConfig` forever. `onboard.cmd` is the only thing that redeems a code,
configures the tools and gives the daemon something to adopt.

A wizard exists to be the setup UI. Handing someone a console at the end of a
GUI install is the defect, and it is the same defect macOS had.

⚠️ **AGENTS.md already carries a correction about this file, and it must not be
re-earned.** A previous doc described an Inno `[Code]` wizard page driving
`keld --json` and claimed its "UX is human-verified on Windows". **That page
never existed.** This document describes a page that does not exist yet either
— it is a design. Nothing here may be restated as shipped until the checklist in
§10 has been walked on a real Windows machine.

## 2. Decision

**One custom Inno wizard page, placed before the install step, driving the
existing `keld` binary over its existing `--json` NDJSON interface — plus one
small helper process that hosts the WebView2 approval panel the page cannot host
itself.**

⚠️ **NOTHING ABOUT AUTH, TOOL DETECTION OR PATH RESOLUTION IS REIMPLEMENTED IN
PASCAL SCRIPT.** The page is a renderer, exactly as `KeldSetup.m` is. Every
decision it draws is made by Go.

**This port needs ZERO changes to `internal/cli`.** Everything the macOS pane
consumes already exists and is already platform-neutral, verified by running the
real binary on Windows 11 (2026-09-15, `go build ./cmd/keld`, `CGO_ENABLED=0`):

```
$ keld.exe whoami --verify --json
{"event":"identity","status":"verified","principal":"dg@keld.co","org":"Keld","api_url":"https://atlas.keld.co"}

$ keld.exe signal setup --dry-run --json
{"event":"tool","name":"claude_code","display":"Claude Code","action":"will_configure","path":"C:\\Users\\dg\\.claude\\settings.json"}
```

`--bin-path`, `--tool`, `--api-url`, `--no-browser` and `--code` all exist on the
commands that need them. The macOS work built this seam; this design consumes
it.

## 3. What was measured (probe, 2026-09-15)

Branch `probe/windows-webview2-embed`, workflow `windows-wv2-probe.yml`.

| # | Claim | Result |
|---|---|---|
| 1 | An Inno custom page hands a usable HWND to another process | **YES.** A `TPanel` on a `CreateCustomPage` page reached a separate process, which read back `class="TPanel"`, the owning PID and the client rect (551x310 at that DPI). |
| 2 | A pure-Go process can host WebView2 inside that foreign HWND | **YES.** Create a `WS_CHILD` under the foreign HWND, then `edge.Chromium.Embed`. CI run 34992310932. |
| 3 | The page really renders, at the panel's size | **YES.** `{"title":"Example Domain","url":"https://example.com/","heading":"Example Domain","ready":"complete","size":"716x452"}` — reported out of the live DOM over the WebView2 message bridge. `716x452` is the panel's client rect exactly, so the viewport IS the panel. |
| 4 | It stays pure Go | **YES.** The host builds at ~3.4 MB with `CGO_ENABLED=0`, so `make crosscheck`'s no-cgo rule survives. No MSVC, no cgo, no C++ DLL. |

⚠️ **THREE MEASUREMENTS PRODUCED A WRONG CONCLUSION FIRST. Each is recorded
because each one silently wastes a day.**

- **`go-webview2`'s `WebViewOptions.Window` IS ACCEPTED AND THEN IGNORED.** The
  field is documented as "creates a new webview using an existing window";
  `NewWithOptions` never reads it, calling `CreateWithOptions`, which always
  `CreateWindowExW`s its own top-level window (`webview.go:320`). The probe
  logged `OK controller created` and found **zero** children under the panel — a
  success report with nothing embedded. Hence `pkg/edge` directly, with the child
  window created by us.
- **A SCREENSHOT CANNOT VERIFY WEBVIEW2.** WebView2 composites through
  DirectComposition (an `Intermediate D3D Window` appears in the tree, owned by
  the GPU process), so `BitBlt` of the host window's DC captures a **blank
  panel** however well the page rendered. The first passing run uploaded exactly
  that, and it read as a failure. The verdict has to be a JS round-trip out of
  the live DOM.
- **`PostQuitMessage` POSTS TO THE CALLING THREAD.** Called from a timer
  goroutine — a different OS thread from the one pumping — neither message loop
  ever saw a quit and the first CI run wedged for its full 15-minute timeout,
  uploading nothing. `PostThreadMessageW` addressed to the loop's own thread id
  is the fix, and both halves carry a watchdog so a wedge can never again cost a
  whole job.

**What is NOT yet measured:** the two halves have only been exercised
separately — Inno's HWND handoff on a dev machine, the embed on CI. Wiring them
together in a real `.iss` is the first implementation task (§9, Task 1), not a
further probe.

## 4. The flow

```
License → Security overview → [ Set up Keld ] → Ready → Installing → Finished
                                ^ our page       ^ ssPostInstall, silent
```

**The page**, top to bottom:

1. **Your Keld account.** On first entry the page runs
   `keld whoami --verify --json` and branches on `status`:
   - `verified` → the field and Connect button are hidden, the row reads
     "Already connected — *principal* · *org*", **Next is enabled**, tools load.
   - `none` / `unauthorized` → the clipboard is tried first (it is instant for
     someone who just clicked Copy on Atlas's download page), and failing that
     the device flow starts and the approval panel appears.
   - `unreachable` → "Can't reach Atlas — Keld can't be set up right now",
     a **Try again** button, **Next stays disabled**.
2. **The approval panel** — Atlas's own `/cli/installer?code=…` route, rendered
   in the WebView2 panel with the user code displayed above it. Hidden again the
   moment `authorized` arrives.
3. **Your AI tools** — the detected tools with checkboxes, all ticked except
   `skipped_conflict`, plus the restart-your-tools line. The page only COLLECTS
   this choice (§7).

⚠️ **THERE IS NO "ANALYSIS ENGINE" ROW, AND ITS ABSENCE IS CORRECT.** macOS
downloads the ~190 MB sidecar in the pane because the pkg cannot carry it past
Apple's notary scan. Windows **bundles** it in the Inno payload
(`keld-agent-sidecar\*`, `ignoreversion recursesubdirs`), so there is nothing to
download, nothing to verify and nothing that can fail. The user sees that work
as part of the ordinary extraction bar. `keld signal install-sidecar` is not
invoked by this installer at all.

**`ssPostInstall`** (no window, no prompts) then:

1. runs `keld signal setup --yes --bin-path {app}\keld.exe --tool …` for the
   ticked tools;
2. runs `keld-agent install --headless` — writes `agent-config.json`, registers
   the logon task, starts the daemon;
3. falls back to today's behaviour when the page never ran (§8).

## 5. Components

### 5.1 The wizard page — `installers/windows/keld-agent.iss` `[Code]`

`CreateCustomPage` after `wpInfoBefore`, so it lands between the security
overview and the Ready page. Controls: a `TNewEdit` + `TNewButton` sharing a
line, a `TNewStaticText` status, a hidden `TNewButton` for Try again, a `TPanel`
for the approval panel, and a `TNewCheckBox` per detected tool.

Next is gated with `WizardForm.NextButton.Enabled`, re-asserted in
`CurPageChanged` because Inno resets it on every page change.

### 5.2 `keld signal setup --bin-path` — already exists, and is load-bearing here too

The page drives `{tmp}\keld.exe`, a path that ceases to exist when the wizard
closes. `keldBinaryPath()` would pin *that* into every tool's hook command and
the failure would be silent — the config looks right, the hook never runs.
`ssPostInstall` passes `--bin-path {app}\keld.exe`.

⚠️ This is the same flag macOS added for the same reason, and it needs no
change: `resolveSetupBinPath` is platform-neutral.

### 5.3 `keld.exe` in `{tmp}` — the Windows equivalent of the plugin's Resources

The payload is not installed when the page runs, so a second `[Files]` entry
stages the CLI for the wizard's own use:

```
Source: "keld.exe"; DestDir: "{app}"; Flags: ignoreversion
Source: "keld.exe"; Flags: dontcopy
```

`ExtractTemporaryFile('keld.exe')` before first use. `dontcopy` files are never
installed; they exist only for `[Code]`.

### 5.4 `keld-wizard-host.exe` — the helper (new)

A new pure-Go command, `cmd/keld-wizard-host`, shipped in the payload and also
staged `dontcopy`. It exists because Pascal Script can host neither a browser nor
an asynchronous child process. It does exactly two things and decides nothing:

- **`--panel <hwnd> --url <url>`** — creates a `WS_CHILD` under the foreign HWND
  and embeds WebView2 on it (§3).
- **`--run <exe> -- <args…> --ndjson <file>`** — runs a `keld` child and streams
  its stdout to `<file>` line by line, which the page tails.

⚠️ **CHILDREN GO IN A JOB OBJECT WITH `JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`.**
A device-flow login polls Atlas for up to its full expiry. Without this, a person
who cancels the wizard mid-sign-in leaves a `keld.exe` polling in the background
with nothing to report to and no way to find it. Inno's `Exec` gives no PID, so
the page cannot kill anything by handle; the job object makes that unnecessary.

⚠️ **AND THE HELPER MUST DIE WHEN THE WIZARD DOES.** It watches for a sentinel
file written by `CancelButtonClick`/`DeinitializeSetup`, and exits — taking its
job, and therefore its children, with it.

**Why a helper rather than redirecting to a file from Pascal.** `Exec(…,
ewNoWait)` plus `cmd /C "keld.exe … > out.ndjson"` looks simpler and is not: it
gives no process handle to cancel, and it depends on the share mode cmd.exe opens
the redirect target with, which is an undocumented detail to hang a login on.
The helper is needed for WebView2 regardless, so `--run` costs one more flag.

### 5.5 WebView2 runtime absence

Evergreen ships with Windows 11 and with Edge on Windows 10, but it is not
guaranteed. `Embed` returning false is detectable, and the page must degrade
rather than dead-end: it hides the panel, opens the same URL in the default
browser with `ShellExec`, and keeps polling. Sign-in still completes without a
console; only the in-wizard rendering is lost.

⚠️ **DO NOT BUNDLE THE EVERGREEN BOOTSTRAPPER TO "FIX" THIS.** It is a
~150 MB download at install time for a case the fallback already covers, and it
would need admin rights this installer deliberately does not ask for
(`PrivilegesRequired=lowest`).

### 5.6 No handoff file

macOS needs `~/.keld/state/installer-handoff.json` because the pane and
`postinstall` are **different processes** — the pane's state cannot otherwise
survive into the privileged script.

⚠️ **WINDOWS NEEDS NONE OF THAT, AND ADDING IT WOULD BE CARGO CULT.** Inno's
`[Code]` runs in one process for the life of the wizard: the variables the page
sets are still there in `CurStepChanged(ssPostInstall)`. So there is no file to
write, none to validate a version against, none to leave behind on a cancelled
install, and none to go stale. The macOS handoff's entire failure surface
(§6 of the macOS design, plus its stale-handoff guard in `postinstall`) does not
exist here.

The consequence for §8 is that "the page never ran" means something much
narrower on Windows, and the fallback changes shape accordingly.

## 6. Ordering rule: nothing destructive before the commit point

Identical to macOS, and forced by the same structure — an Inno custom page can
only be created ahead of `wpReady`, so a person can still cancel when it runs.

- **Allowed in the page:** redeeming the code (writes `auth.json` — a token, and
  a cancelled install leaves it inert) and reading which tools are installed
  (`--dry-run` writes nothing).
- **Not allowed in the page:** rewriting a tool's `settings.json`, writing
  `hook.json`, registering the logon task. Those happen in `ssPostInstall`,
  after the person has committed.

The cost is that per-tool results are not visible — the page says "will
configure", not "configured". Same trade as macOS, stated rather than hidden.

## 7. All-or-nothing

**Next is enabled by exactly one thing: a VERIFIED connection to Atlas** —
either a setup code Atlas accepted, or `whoami --verify` confirming the stored
credential still works. There is no "set up later".

⚠️ **`keld whoami` WITHOUT `--verify` CANNOT ANSWER THIS.** It prints what a
local file says and never contacts Atlas, so a revoked token and a live one are
indistinguishable to it. Gating Next on auth.json's existence would let someone
install with a dead credential and collect nothing.

The three failure states stay apart deliberately: `unauthorized` means a code is
required, `unreachable` means we learned nothing and the install must not
proceed, `none` means a fresh machine.

⚠️ **THE WINDOWS ESCAPE HATCH IS REAL, WHICH IS WHY THE GATE CAN BE THIS HARD.**
macOS argued all-or-nothing partly because the CLI is a terminal and the app is
unreleased. On Windows `keld login` from a terminal genuinely works — but that is
the thing this design exists to stop people needing, so it is a recovery path,
not a designed one, and the page does not mention it.

## 8. Failure modes

| Failure | Behaviour |
|---|---|
| Code rejected | Stated inline from the NDJSON `error`; Next stays disabled; Try again, or paste another code. |
| Atlas unreachable | Stated; Next disabled; **Try again** is the only way on. No button proceeds unverified. |
| Device flow expires or is declined | Stated; the panel closes; Try again, or paste a setup code. |
| No WebView2 runtime | Panel hidden, same URL opened in the default browser, polling continues (§5.5). |
| Wizard cancelled mid-sign-in | The helper's sentinel fires; job object reaps the `keld` child. `auth.json` may exist and is inert. |
| `/SILENT` (MDM) | The page is never shown. `[Run]` entry 1 still registers the agent `--headless`; the machine idles on `awaitConfig` and is finished with `keld-agent install --code <CODE>` from the management tool. **Unchanged from today.** |
| Page raised an exception / `[Code]` failed to run | `ssPostInstall` finds no recorded pairing and falls back to opening `onboard.cmd`, exactly as today. |

⚠️ **THE FALLBACK IS NARROWER THAN macOS'S AND MUST NOT BE COPIED VERBATIM.**
macOS gates on *both* "no handoff file" AND "no `hook.json`", because its pane
can fail to load with no diagnostic at all and because "Set up later" was a
reachable choice. Neither applies here: there is no separate process to fail to
load (§5.6), and there is no "Set up later" (§7). So the Windows condition is
simply **the page did not complete a pairing** — a module-level Boolean this same
process set — and `/SILENT`, where the page never runs by design, keeps
`skipifsilent` doing that job.

## 9. Testing

**What CI can verify** — static assertions, extending
`installers/windows/keld_agent_iss_test.sh`, which already exists and already
guards this file:

- `keld.exe` is staged `dontcopy` as well as installed, or the page has nothing
  to drive;
- the page calls `ExtractTemporaryFile` before its first `Exec`;
- `ssPostInstall` passes `--bin-path`, or every hook it writes is broken;
- the `[Run]` registration entry still says `--headless` and is still neither
  `postinstall` nor `skipifsilent` (existing assertions, unchanged);
- `onboard.cmd` is still staged and still not `runhidden` (existing);
- the page does not parse, branch on, or reimplement anything the NDJSON
  already decides — asserted by grepping the `[Code]` block for the command
  names it is allowed to run.

**Go tests:** `cmd/keld-wizard-host`'s argument parsing, NDJSON relay and
sentinel handling. The window/WebView2 half has no unit test — it needs a
desktop — and is covered by the probe workflow instead.

**`iscc` compiles the script in CI**, which proves the files are staged and the
Pascal parses. It proves nothing about behaviour.

⚠️ **WHAT NO CI CHECK CAN VERIFY:** that the page appeared and a human could use
it. This is the same limit `plugin_test.sh` states for macOS. The wizard is
driven by hand on a real Windows machine before each release.

⚠️ **AND IT CANNOT BE DRIVEN BY HAND ON EVERY MACHINE.** Smart App Control — on
by default on clean Windows 11 installs — blocked every freshly-built unsigned
binary that creates windows and spawns processes on the dev machine used for
this design, and blocked a compiled Inno `setup.exe` besides. SAC cannot be
re-enabled once disabled without resetting Windows. A machine for manual
verification either has SAC off already or must be a VM.

## 10. The manual checklist, in order

1. The "Set up Keld" page appears after the security overview and before Ready.
2. With a valid code on the clipboard: the row goes green **without anyone
   typing**, and Next enables.
3. With nothing on the clipboard: the approval panel appears, shows a user code,
   and the code on screen **matches the code on the page inside the panel**.
4. Signing in inside the panel turns the row green and enables Next.
5. A bad code is refused inline and **Next stays disabled**.
6. With the network off: "Can't reach Atlas", Next disabled, Try again works
   once the network returns.
7. The tool list shows what is installed, ticked, with a conflicted tool
   unticked and labelled.
8. After the install: `%USERPROFILE%\.keld\hook.json` holds an `ingest_token`,
   `schtasks /query /tn KeldAgent` shows the job, the configured tools' settings
   point at `127.0.0.1:14318`, and **no console window ever opened**.
9. Cancel mid-sign-in, then check Task Manager: **no orphaned `keld.exe`**.
10. `/SILENT`: no page, no console, task registered, machine on `awaitConfig`.

## 11. Signing — MEASURED AS A HARD BLOCKER, not a nice-to-have

⚠️ **THIS SECTION UNDERSTATED THE PROBLEM AND IS CORRECTED RATHER THAN EDITED.**
It read "this design adds a second unsigned executable to the payload" and
treated signing as an open decision to take before shipping. Driving the real
wizard on a Smart App Control machine (2026-09-15) showed it is load-bearing for
the feature itself:

```
run start mode=1 args=whoami --verify --json
run finished mode=1 msg=could not start C:\…\is-PVEZHWTRPH.tmp\keld.exe:
  An Application Control policy has blocked this file.
```

**SAC blocks the `keld.exe` the page extracts to `{tmp}` and drives** (§5.3), so
identity, login and tool detection all fail to START. Nothing about the page is
wrong; it simply has nothing to drive. The same binary run from a normal
directory is allowed, so this is not about the bytes — it is about an unsigned
binary extracted by an installer into a temp directory.

Consequences worth stating plainly:

- **The wizard cannot work on a SAC machine until `keld.exe` is signed.** Not
  degraded — inoperative. The console fallback (§8) is what such a machine gets,
  which is exactly the outcome this design exists to remove.
- **Signing `keld-setup.exe` alone is not enough.** The blocked file is the
  payload binary, not the installer. `keld.exe`, `keld-agent.exe` and
  `keld-wizard-host.exe` all need it.
- **The failure was originally reported to the person as "Sign-in didn't
  finish"** — a confident negative from a check that never ran. The page now
  distinguishes a run that FAILED TO START from one that answered no, and says
  which; an empty `identity` status is treated as the absence of an answer rather
  than a fourth kind of answer.

## 11a. What signing does not fix, and should not hide

`keld-setup.exe` is **unsigned** — `installers.yml` has no Windows signing step —
and Smart App Control blocks unsigned installers (§9). This design adds a second
unsigned executable (`keld-wizard-host.exe`) to the payload.

⚠️ **THIS IS NOT A REGRESSION INTRODUCED HERE AND IT IS NOT FIXED HERE EITHER.**
It is called out because it is the difference between "the wizard is nicer" and
"the wizard runs at all", and because the failure is silent in the same way the
macOS stale-signature failure was: SAC's block is a dialog, not a log line, and
nothing reports it back. Authenticode signing for the Windows installer is its
own piece of work with its own certificate procurement, and it should be decided
before this ships rather than discovered after.

## 12. Out of scope, and what stays

- **`onboard.cmd` is NOT deleted.** It remains the `/SILENT`-adjacent fallback
  (§8), the MDM path, and the thing that keeps this change from being able to
  break the install that works today. It stops being opened on the success path.
- **`keld-agent install --code` is unchanged** — MDM and the PowerShell
  installer depend on it.
- **`internal/cli` is unchanged.** Every seam this consumes already exists (§2).
- **The analysis sidecar stays bundled.** No download, no progress row, no
  `install-sidecar` call (§4).
- **Windows installer signing** (§11) — named, not solved.
- **The desktop app.** Pre-alpha, not part of this flow, not named in any
  user-visible string.
