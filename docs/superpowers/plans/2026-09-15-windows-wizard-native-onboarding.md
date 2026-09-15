# Windows Wizard-Native Onboarding Implementation Plan

**Design:** `docs/superpowers/specs/2026-09-15-windows-wizard-native-onboarding-design.md`
**Probe:** branch `probe/windows-webview2-embed`, workflow `windows-wv2-probe.yml`, CI run 34992310932.

Removes the console from the Windows install. Onboarding moves into a custom Inno
wizard page that renders the NDJSON `keld` already emits, with one helper process
for the two things Pascal Script cannot do: host a browser, and run a child
asynchronously.

## Global Constraints

- **`internal/cli` DOES NOT CHANGE.** Every seam consumed here already exists and
  was verified by running the real binary on Windows 11 (design §2). A task that
  finds itself editing `internal/cli` has taken a wrong turn — the macOS work
  built this seam, and a second consumer is the point of it.
- **Pure Go, `CGO_ENABLED=0`.** `make crosscheck` enforces it for release targets
  and the helper is one. No cgo, no MSVC, no C++ DLL.
- **The helper renders and relays; it decides nothing.** Same rule as
  `KeldSetup.m`. No auth logic, no tool detection, no path resolution.
- **Nothing destructive before `ssPostInstall`** (design §6).
- **No handoff file** (design §5.6). `[Code]` is one process; module-level
  variables carry the page's state into `CurStepChanged`.
- **Every assertion added to `keld_agent_iss_test.sh` must be tested against a
  deliberately broken file.** That script's own header records an assertion that
  passed vacuously for its whole first run; the fix was testing the guard, not
  trusting it.
- Run `go test ./...` and `bash installers/windows/keld_agent_iss_test.sh` before
  claiming any task passes, and paste the output.

---

### Task 1: `cmd/keld-wizard-host` — `--run` mode

The page needs a `keld` child that runs asynchronously, streams NDJSON somewhere
tailable, and cannot outlive the wizard. Inno's `Exec` gives no PID, so
cancellation cannot work by handle.

**Files:**
- Create: `cmd/keld-wizard-host/main.go`
- Create: `cmd/keld-wizard-host/run.go`
- Test: `cmd/keld-wizard-host/run_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `keld-wizard-host.exe --run <exe> --ndjson <file> [--sentinel <file>] -- <args…>`
  — runs `<exe> <args…>`, appends each stdout line to `<file>`, exits when the
  child exits or `<sentinel>` appears. Exit code is the child's.

- [ ] **Step 1: Write the failing tests**

```go
// cmd/keld-wizard-host/run_test.go
package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// relay must write lines AS THEY ARRIVE, not on exit. The page tails this file
// to move a status label; a relay that buffers until the child exits turns a
// ten-minute device flow into a frozen page.
func TestRelayWritesLinesIncrementally(t *testing.T) {
	dir := t.TempDir()
	ndjson := filepath.Join(dir, "out.ndjson")

	done := make(chan error, 1)
	go func() {
		done <- relay(runOpts{
			Exe:    osShell(),
			Args:   osEcho(`{"event":"a"}`, `{"event":"b"}`),
			NDJSON: ndjson,
		})
	}()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(ndjson); err == nil && strings.Count(string(b), "\n") >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := <-done; err != nil {
		t.Fatalf("relay: %v", err)
	}
	b, err := os.ReadFile(ndjson)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := strings.Count(string(b), "\n")
	if got < 2 {
		t.Fatalf("relayed %d lines, want 2: %q", got, b)
	}
}

// ⚠️ A partial final line must not be dropped. `keld login --json` writes
// `authorized` LAST, and the pane on macOS learned the hard way that losing the
// final line means "paired, and the UI says it failed".
func TestRelayKeepsUnterminatedFinalLine(t *testing.T) {
	dir := t.TempDir()
	ndjson := filepath.Join(dir, "out.ndjson")
	if err := relay(runOpts{
		Exe:    osShell(),
		Args:   osPrintNoNewline(`{"event":"authorized"}`),
		NDJSON: ndjson,
	}); err != nil {
		t.Fatalf("relay: %v", err)
	}
	b, _ := os.ReadFile(ndjson)
	if !strings.Contains(string(b), `"authorized"`) {
		t.Fatalf("final unterminated line was dropped: %q", b)
	}
	if !strings.HasSuffix(string(b), "\n") {
		t.Fatalf("relayed file must end in a newline so a tailer sees a complete line: %q", b)
	}
}

// The sentinel is how a cancelled wizard stops a device-flow poll that would
// otherwise run for its full expiry with nobody listening.
func TestSentinelStopsTheRun(t *testing.T) {
	dir := t.TempDir()
	ndjson := filepath.Join(dir, "out.ndjson")
	sentinel := filepath.Join(dir, "cancel")

	done := make(chan error, 1)
	go func() {
		done <- relay(runOpts{
			Exe:      osShell(),
			Args:     osSleep(60),
			NDJSON:   ndjson,
			Sentinel: sentinel,
		})
	}()

	time.Sleep(300 * time.Millisecond)
	if err := os.WriteFile(sentinel, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Fatal("sentinel did not stop the run")
	}
}

func TestRunRequiresExeAndNDJSON(t *testing.T) {
	if err := relay(runOpts{NDJSON: "x"}); err == nil {
		t.Fatal("missing --run should be an error")
	}
	if err := relay(runOpts{Exe: osShell()}); err == nil {
		t.Fatal("missing --ndjson should be an error")
	}
}

func osShell() string {
	if runtime.GOOS == "windows" {
		return "cmd.exe"
	}
	return "/bin/sh"
}
// osEcho / osPrintNoNewline / osSleep build per-OS argv for the helpers above;
// keep them in the test file so the relay itself stays OS-agnostic.
```

- [ ] **Step 2: Run the tests — they must fail**

```
go test ./cmd/keld-wizard-host/
```

- [ ] **Step 3: Implement `relay`**

`runOpts{Exe, Args, NDJSON, Sentinel}`. Open the NDJSON file
`O_CREATE|O_WRONLY|O_APPEND` with `FILE_SHARE_READ` semantics (Go's default on
Windows permits concurrent readers). Scan the child's stdout with a
`bufio.Scanner`, write each line plus `\n`, and `Sync()` after each — a tailer
that reads a half-flushed buffer sees a truncated JSON line and drops it.

⚠️ **Create the child inside a job object with
`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`** (`golang.org/x/sys/windows`). Without it a
device-flow `keld login` survives a cancelled wizard and polls Atlas for its full
expiry with nothing to report to. `CREATE_SUSPENDED` → assign → resume, so the
child cannot exec before it is in the job.

Poll `Sentinel` every 250 ms on a goroutine; when it appears, close the job.

- [ ] **Step 4: Run the tests — they must pass**

- [ ] **Step 5: Verify on Windows against the real binary**

```
go build -o keld-wizard-host.exe ./cmd/keld-wizard-host
.\keld-wizard-host.exe --run .\keld.exe --ndjson out.ndjson -- whoami --verify --json
type out.ndjson
```

Expect one `identity` line. Paste it.

---

### Task 2: `cmd/keld-wizard-host` — `--panel` mode

Lift the probe's host verbatim (branch `probe/windows-webview2-embed`,
`installers/windows/probe/cmd/host`). It is already measured; this task is
packaging, not discovery.

**Files:**
- Create: `cmd/keld-wizard-host/panel_windows.go`
- Create: `cmd/keld-wizard-host/panel_other.go` (build-tagged stub so the package
  still builds on the dev machines that run `go test ./...` on Linux/macOS)

**Interfaces:**
- Produces: `--panel <hwnd> --url <url> [--sentinel <file>]` — creates a
  `WS_CHILD` under `<hwnd>`, embeds WebView2, navigates, pumps its own message
  loop. Exit code 0 on clean exit, non-zero when the runtime is absent.

- [ ] **Step 1: Port the probe's host**

Keep these three, each of which cost a run to learn (design §3):

- **`pkg/edge` directly, never `webview2.NewWithOptions`.** The `Window` option is
  accepted and ignored; it creates its own top-level window and reports success.
- **`PostThreadMessageW` to the loop's own thread id**, captured beside
  `runtime.LockOSThread()`. `PostQuitMessage` posts to the calling thread and a
  timer goroutine is not it.
- **A watchdog `time.AfterFunc` that force-exits.** A wedged message loop
  otherwise hangs whatever is waiting on it.

- [ ] **Step 2: Report runtime absence as a distinct exit code**

`Embed` returning false must exit **3**, not 1, so the page can tell "no WebView2
runtime" (fall back to the browser, design §5.5) from "something else broke".

- [ ] **Step 3: Verify — re-run the probe workflow against the real command**

Point `windows-wv2-probe.yml`'s harness at `cmd/keld-wizard-host` instead of the
probe's copy. The PASS line to look for is the JS round-trip, **not** the
screenshot:

```
PASS page reported from inside the embedded control: {"title":…,"ready":"complete","size":"716x452"}
```

⚠️ A blank `panel.png` is expected and proves nothing either way — WebView2
composites through DirectComposition and `BitBlt` cannot photograph it.

---

### Task 3: The wizard page — skeleton, identity, Next gating

**Files:**
- Modify: `installers/windows/keld-agent.iss` (`[Files]`, `[Code]`)

- [ ] **Step 1: Stage the CLI and the helper for the wizard's own use**

```
Source: "keld.exe";              Flags: dontcopy
Source: "keld-wizard-host.exe";  DestDir: "{app}"; Flags: ignoreversion
Source: "keld-wizard-host.exe";  Flags: dontcopy
```

⚠️ The `dontcopy` copies are what the page drives; the payload is not installed
when it runs. `ExtractTemporaryFile` both before first use.

- [ ] **Step 2: Create the page**

`CreateCustomPage(wpInfoBefore, 'Set up Keld', 'Sign this device in to your Keld account.')`.
Controls per design §5.1.

- [ ] **Step 3: The NDJSON pump**

One helper procedure: start `keld-wizard-host --run`, then loop
`Sleep(200); AppProcessMessages;` reading new lines from the NDJSON file with
`LoadStringsFromFile`, tracking a consumed-count so each line is handled once.
Dispatch on the `event` field with simple string matching.

⚠️ **`AppProcessMessages` in the loop is not optional.** Without it the wizard
stops repainting for the whole of a device flow and Windows marks it Not
Responding.

⚠️ **Handle only the four event names the design names** — `identity`,
`device_code`, `authorized`, `error`. An unrecognised line is ignored, never
guessed at.

- [ ] **Step 4: Identity on entry**

In `CurPageChanged`, once per wizard run: `whoami --verify --json`, branch on
`status` per design §4 — `verified` / `unreachable` / `none` / `unauthorized`.

⚠️ **Re-assert `WizardForm.NextButton.Enabled` in `CurPageChanged`.** Inno resets
it on every page change, so a gate set once leaks open when the user goes Back
and forward again.

- [ ] **Step 5: Verify by hand on Windows**

Checklist items 1 and 6 from design §10. Screenshot both states.

---

### Task 4: Clipboard prefill, device flow, approval panel

- [ ] **Step 1: Clipboard first**

`GetClipboardText`, validated against the same shape
`KeldLooksLikePairingCode` accepts (`installers/macos/plugin/KeldCode.m` — port
the predicate, not a new one; the two must agree about what a code looks like).
On a match, fill the field and connect without asking.

⚠️ **Validate before connecting.** Firing a login attempt at arbitrary copied
text is how a clipboard convenience becomes a rejected-code error nobody caused.

- [ ] **Step 2: Device flow**

`keld login --json --no-browser` via the pump.

⚠️ **`--no-browser` is load-bearing.** Without it the CLI opens the verification
URL itself, putting the same approval in two places at once — one of them the
browser this design exists to avoid.

On `device_code`: show `user_code`, prefer `installer_url` over
`verification_url`, and launch `--panel` with the `TPanel`'s `Handle`.

⚠️ **`installer_url` is absent on an older Atlas and that must still work** —
fall back, never load an empty URL.

- [ ] **Step 3: Display the user code**

⚠️ **Not decoration.** Device flow's protection against approving somebody else's
sign-in is that the code on this screen matches the code in the panel. A spinner
alone throws that away.

- [ ] **Step 4: Borrow the space, don't grow the page**

An Inno wizard page has a fixed height. Hide the tool section and the code field
while the panel is up, restore them on `authorized` — the same stow-and-restore
`showApprovalPage`/`hideApprovalPage` do on macOS.

- [ ] **Step 5: WebView2 absent → browser fallback**

Helper exit code 3 → hide the panel, `ShellExec` the same URL, keep polling.

- [ ] **Step 6: Cancellation**

`CancelButtonClick` and `DeinitializeSetup` write the sentinel. Task 1's job
object does the rest.

- [ ] **Step 7: Verify by hand**

Checklist items 2, 3, 4, 5, 9.

---

### Task 5: The tool list

- [ ] **Step 1:** On pairing, run `signal setup --dry-run --json` through the
  pump and render one checkbox per `tool` event.
- [ ] **Step 2:** Tick everything except `skipped_conflict`, which is unticked and
  labelled "— has its own telemetry settings; leave it alone".
- [ ] **Step 3:** Add the restart line: tools read their settings once, at
  startup, and nothing on the machine can detect or fix a tool that was already
  running.
- [ ] **Step 4:** Verify — checklist item 7.

---

### Task 6: `ssPostInstall`, and retiring the console

**Files:**
- Modify: `installers/windows/keld-agent.iss` (`[Run]`, `[Code]`)

- [ ] **Step 1: Do the destructive work**

In `CurStepChanged(ssPostInstall)`, when the page paired:

```
{app}\keld.exe signal setup --yes --bin-path {app}\keld.exe [--api-url <url>] --tool <t>…
{app}\keld-agent.exe install --headless
```

⚠️ **`--bin-path` is mandatory.** The page drove `{tmp}\keld.exe`; pinning that
into hook commands breaks every hook the moment the wizard closes, silently.

⚠️ **An empty selection configures NOTHING, not everything.** `tools.Select(nil)`
reads "no `--tool` flags" as "every detected adapter". Correct when the page never
ran; wrong when the person unticked everything. Track "the page ran" separately
from "the list is empty" — the same distinction `had_handoff` draws on macOS,
here just a Boolean in the same process.

- [ ] **Step 2: Take `onboard.cmd` off the success path**

The `[Run]` entry becomes conditional: `Check:` a function returning True only
when the page did not complete a pairing.

⚠️ **KEEP THE ENTRY, AND KEEP `skipifsilent`.** It is the `/SILENT` path's
behaviour and the fallback for a `[Code]` failure. Deleting it is how a
`/SILENT` fleet goes dark.

⚠️ **DO NOT ADD `runhidden`.** AGENTS.md records what that cost: an interactive
login in a window nobody could see, every machine idling on `awaitConfig`
forever.

⚠️ **Registration entry 1 is unchanged** — always runs, `--headless`, not
`postinstall`, not `skipifsilent`.

- [ ] **Step 3: Verify** — checklist items 8 and 10.

---

### Task 7: Build and ship the helper

**Files:**
- Modify: `.github/workflows/installers.yml` (Windows job)
- Modify: `.goreleaser.yaml` if the helper should also ship in the archives

- [ ] **Step 1:** Build `keld-wizard-host.exe` with `CGO_ENABLED=0` and copy it
  beside `keld.exe`/`keld-agent.exe` into `installers\windows\` before `iscc`.
- [ ] **Step 2:** Confirm `make crosscheck` still passes with the new target.
- [ ] **Step 3:** Run `make release-dry` and install the resulting artifact on a
  test machine, paired with an **atlas-dev** setup code so no machine lands in
  the production org.

---

### Task 8: CI guards

**Files:**
- Modify: `installers/windows/keld_agent_iss_test.sh`

- [ ] **Step 1: Add the assertions** (design §9)

```bash
# The page drives a copy of keld from {tmp}; the payload is not installed yet.
grep -qE 'Source: "keld\.exe";[^\n]*dontcopy' "$iss" || \
  fail "keld.exe is not staged dontcopy — the wizard page has nothing to drive"

grep -q 'ExtractTemporaryFile' "$iss" || \
  fail "no ExtractTemporaryFile — a dontcopy file is not on disk until it is extracted"

# Without --bin-path every hook pins {tmp}\keld.exe, which stops existing when
# the wizard closes. The config looks right and the hook never runs.
code_block="$(sed -n '/^\[Code\]/,$p' "$iss")"
printf '%s\n' "$code_block" | grep -q -- '--bin-path' || \
  fail "ssPostInstall omits --bin-path — every tool hook would pin a temp path"

# onboard.cmd must survive as the fallback, and must stay conditional.
printf '%s\n' "$entries" | grep -F 'onboard.cmd' | grep -q 'Check:' || \
  fail "onboard.cmd is unconditional — it would open on the success path too"
```

- [ ] **Step 2: Test every new assertion against a deliberately broken copy**

```
cp installers/windows/keld-agent.iss /tmp/broken.iss
# delete the --bin-path, re-run, confirm it FAILS
```

⚠️ **This step is the task.** That script's header records an assertion that
reported a clean bill on a file whose flags it had never looked at, because the
backslash continuations were never unfolded. An untested guard is worse than no
guard: it is a guard everyone believes.

- [ ] **Step 3:** Confirm `iscc` still compiles the script in CI.

---

### Task 9: Documentation

- [ ] **Step 1:** `docs/windows-wizard-onboarding.md` — the runbook, mirroring
  `docs/macos-wizard-onboarding.md`, with its own "things that fail silently"
  section: a `dontcopy` file not extracted, a Next gate not re-asserted in
  `CurPageChanged`, a `--bin-path` omission, and SAC blocking an unsigned
  installer.
- [ ] **Step 2:** AGENTS.md — replace the **Windows onboarding UI** bullet.

⚠️ **The correction in that bullet stays.** It records that a previous doc
described an Inno wizard page as shipped when none existed. Rewrite it to
describe what now ships and **keep the history** — that correction is why this
plan exists.

- [ ] **Step 3:** CHANGELOG.md.
- [ ] **Step 4:** Delete the probe — branch `probe/windows-webview2-embed` and
  `windows-wv2-probe.yml`. Its findings live in the design doc; the code was
  scaffolding.

---

## Open decision, to be made before Task 7 ships

**Authenticode signing for `keld-setup.exe`** (design §11). It is unsigned today,
Smart App Control blocks unsigned installers, and this plan adds a second
unsigned executable to the payload. Not fixed here; it needs a certificate and
its own work. Deciding it after release means discovering it as "the installer
does nothing when I double-click it", with no log line anywhere.
