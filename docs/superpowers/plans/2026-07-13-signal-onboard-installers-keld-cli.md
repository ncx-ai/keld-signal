# Token-aware CLI installers (keld-cli) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Make `keld-agent install` code-aware (`--code`/`KELD_SETUP_CODE`) and fix its TTY detection, then rewire `install.sh`, `install.ps1`, and `onboard.command` to call `keld-agent install [--code X]` so the curl|sh / irm|iex / .pkg installers all onboard the user (login → setup → agent-last) with the pre-auth setup code.

**Architecture:** The orchestrator already exists — `internal/agentcli/agentcli.go`'s `runInstall` runs `keld login` → `keld signal setup` → `service.Install`, TTY-aware. We extend it (code branch + flag/env plumbing + stdout-based TTY) rather than add a new command. The installer scripts collapse to a single `keld-agent install [--code X]` call.

**Tech Stack:** Go + cobra + `golang.org/x/term`; POSIX sh; PowerShell; `shellcheck`.

**Spec:** `docs/superpowers/specs/2026-07-13-signal-onboard-installers-design.md`.

## Global Constraints

- No change to `keld login`, `keld signal setup`, or the enroll wire contract — `keld-agent install` composes them via subprocess (`stepRunner`, inherited stdio). No new CLI command.
- `--code` flag wins over the `KELD_SETUP_CODE` env var; a resolved code makes onboarding **headless-capable** (runs regardless of TTY).
- Agent starts **last**: `service.Install()` runs after login+setup (the daemon refuses to run without `~/.keld/hook.json`).
- A login/setup failure in the code or TTY branch must abort **before** `service.Install()`.
- TTY detection keys on **stdout** (`os.Stdout`), not stdin — under `curl | sh` stdin is the pipe.
- Keep the four existing `TestRunInstall*` cases green (signatures widen; behavior for the no-arg case is unchanged).
- Commit trailer: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`.
- Go test: `go test ./internal/agentcli/...` (and `go test ./...` before the final commit). Shell: `shellcheck scripts/install.sh installers/macos/onboard.command`.

---

## Task 1: code-aware `keld-agent install`

**Files:**
- Modify: `internal/agentcli/agentcli.go` (`runInstall` signature+body, `stdinIsTTY`→`stdoutIsTTY`, the `install` cobra command)
- Test: `internal/agentcli/agentcli_test.go` (widen the 4 existing cases; add code/passthrough cases)

**Interfaces:**
- Consumes: existing `resolveKeld() (string, error)`, `stepRunner`, `runStep`, `service.Install`.
- Produces: `type installConfig struct { code, apiURL string; yes, jsonOut bool }`; `func runInstall(cfg installConfig, isTTY func() bool, resolveKeld func() (string, error), run stepRunner, installService func() error) error`; `func stdoutIsTTY() bool`.

- [ ] **Step 1: Widen the existing tests to the new signature + add the code/TTY matrix.**
  Replace the four existing `runInstall(...)` call sites so they pass `installConfig{}` as the first argument, and add the new cases. In `internal/agentcli/agentcli_test.go`, update/extend:

```go
// helper: records "name arg arg" per call
func recorder() (*[]string, stepRunner) {
	var calls []string
	return &calls, func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
}

func TestRunInstallSequence(t *testing.T) { // no code, TTY → login, signal setup (no --yes)
	calls, run := recorder()
	installed := false
	err := runInstall(installConfig{}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, func() error { installed = true; return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{"/fake/keld login", "/fake/keld signal setup"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("steps = %v, want %v", *calls, want)
	}
	if !installed {
		t.Fatal("service install was not called")
	}
}

func TestRunInstallWithCodeIsHeadlessCapable(t *testing.T) { // code set, no TTY → still onboards
	calls, run := recorder()
	installed := false
	err := runInstall(installConfig{code: "AB12-CD34"}, func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, func() error { installed = true; return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{"/fake/keld login --code AB12-CD34", "/fake/keld signal setup --yes"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("steps = %v, want %v", *calls, want)
	}
	if !installed {
		t.Fatal("service install must run after code onboarding")
	}
}

func TestRunInstallCodeAbortsBeforeService(t *testing.T) {
	var calls []string
	run := func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		if strings.Contains(calls[len(calls)-1], "login") {
			return errors.New("bad code")
		}
		return nil
	}
	installed := false
	err := runInstall(installConfig{code: "NOPE"}, func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, func() error { installed = true; return nil })
	if err == nil {
		t.Fatal("expected error when login --code fails")
	}
	if len(calls) != 1 || installed {
		t.Fatalf("must stop after login; calls=%v installed=%v", calls, installed)
	}
}

func TestRunInstallApiURLAndJSONPassthrough(t *testing.T) {
	calls, run := recorder()
	err := runInstall(installConfig{code: "X1-Y2", apiURL: "http://localhost:8000", jsonOut: true},
		func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{
		"/fake/keld login --api-url http://localhost:8000 --json --code X1-Y2",
		"/fake/keld signal setup --api-url http://localhost:8000 --json --yes",
	}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("steps = %v, want %v", *calls, want)
	}
}

func TestRunInstallYesInTTY(t *testing.T) { // no code, TTY, yes=true → setup --yes
	calls, run := recorder()
	err := runInstall(installConfig{yes: true}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{"/fake/keld login", "/fake/keld signal setup --yes"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("steps = %v, want %v", *calls, want)
	}
}
```
  Keep `TestRunInstallAbortsWhenKeldMissing` and `TestRunInstallNoTTYSkipsLoginAndSetup`, updating their `runInstall(` calls to pass `installConfig{}` as the first arg (no code + no TTY still skips; missing keld in the TTY branch still errors before any run/install).

- [ ] **Step 2: Run → fail.**
  Run: `go test ./internal/agentcli/... -run RunInstall -v`
  Expected: compile error / FAIL — `installConfig` undefined and `runInstall` arity mismatch.

- [ ] **Step 3: Implement in `internal/agentcli/agentcli.go`.**
  Replace `stdinIsTTY` (agentcli.go:93-101) with a stdout-based detector:

```go
// stdoutIsTTY reports whether stdout is an interactive terminal. Detection keys on
// stdout, NOT stdin: under `curl | sh` the installer — and the keld-agent it spawns —
// inherit the pipe as stdin, so a stdin check misreads a human in a real terminal as
// headless. Interactive device-flow login needs no stdin (it prints a URL and polls),
// so a piped stdin never blocks it. A GUI installer (launchd/runhidden) has no terminal
// on stdout either, so the headless branch is still selected there.
func stdoutIsTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}
```
  Replace `runInstall` (agentcli.go:72-91) with the code-aware version:

```go
// installConfig carries the install command's onboarding knobs.
type installConfig struct {
	code    string // one-time setup code; when set, onboarding runs headless
	apiURL  string // --api-url passthrough for local dev
	yes     bool   // pass --yes to signal setup (implied when code is set)
	jsonOut bool   // --json passthrough for installer UIs
}

// runInstall sets the user up, then registers the service. Order matters: the daemon
// refuses to run until signal setup has written ~/.keld/hook.json, and installService
// starts it immediately — so service install runs last. With a setup code the login+setup
// run non-interactively regardless of TTY; without a code they run only in a real terminal.
func runInstall(cfg installConfig, isTTY func() bool, resolveKeld func() (string, error), run stepRunner, installService func() error) error {
	login := []string{"login"}
	setup := []string{"signal", "setup"}
	if cfg.apiURL != "" {
		login = append(login, "--api-url", cfg.apiURL)
		setup = append(setup, "--api-url", cfg.apiURL)
	}
	if cfg.jsonOut {
		login = append(login, "--json")
		setup = append(setup, "--json")
	}

	switch {
	case cfg.code != "":
		keld, err := resolveKeld()
		if err != nil {
			return err
		}
		login = append(login, "--code", cfg.code)
		setup = append(setup, "--yes")
		if err := run(keld, login...); err != nil {
			return fmt.Errorf("keld login: %w", err)
		}
		if err := run(keld, setup...); err != nil {
			return fmt.Errorf("keld signal setup: %w", err)
		}
	case isTTY():
		keld, err := resolveKeld()
		if err != nil {
			return err
		}
		if cfg.yes {
			setup = append(setup, "--yes")
		}
		if err := run(keld, login...); err != nil {
			return fmt.Errorf("keld login: %w", err)
		}
		if err := run(keld, setup...); err != nil {
			return fmt.Errorf("keld signal setup: %w", err)
		}
	default:
		fmt.Println("Service installed. Finish setup by running: keld login && keld signal setup")
	}
	return installService()
}
```
  Rewire the `install` cobra command (agentcli.go:121-127) to parse flags + the env default and call the new signature:

```go
installCmd := &cobra.Command{
	Use:   "install",
	Short: "Log in, set up telemetry, and install keld-agent as a per-user autostart service.",
	RunE: func(cmd *cobra.Command, args []string) error {
		code, _ := cmd.Flags().GetString("code")
		if code == "" {
			code = os.Getenv("KELD_SETUP_CODE") // flag wins; fall back to the env var
		}
		yes, _ := cmd.Flags().GetBool("yes")
		apiURL, _ := cmd.Flags().GetString("api-url")
		jsonOut, _ := cmd.Flags().GetBool("json")
		cfg := installConfig{code: code, apiURL: apiURL, yes: yes, jsonOut: jsonOut}
		return runInstall(cfg, stdoutIsTTY, resolveKeld, runStep, service.Install)
	},
}
installCmd.Flags().String("code", "", "Redeem a one-time setup code for a non-interactive login (defaults to $KELD_SETUP_CODE).")
installCmd.Flags().Bool("yes", false, "Skip confirmation prompts during setup.")
installCmd.Flags().String("api-url", "", "Target a different Keld API base URL (e.g. http://localhost:8000) for local dev.")
installCmd.Flags().Bool("json", false, "Emit machine-readable NDJSON from login/setup (for installer UIs).")
root.AddCommand(installCmd)
```

- [ ] **Step 4: Run → pass.**
  Run: `go test ./internal/agentcli/... -v`
  Expected: PASS (all `TestRunInstall*` + `TestKeldInDir`).

- [ ] **Step 5: Full build + vet.**
  Run: `go build ./... && go vet ./internal/agentcli/... && go test ./...`
  Expected: builds clean; whole suite passes.

- [ ] **Step 6: Commit.**

```bash
git add internal/agentcli/agentcli.go internal/agentcli/agentcli_test.go
git commit -m "feat(agent): keld-agent install --code / KELD_SETUP_CODE + stdout TTY fix

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: rewire the installer scripts

**Files:**
- Modify: `scripts/install.sh` (parse a code; call `keld-agent install [--code]`)
- Modify: `scripts/install.ps1` (`-Code` param + `$env:KELD_SETUP_CODE`; call `keld-agent install [-Code]` if `keld-agent.exe` present)
- Modify: `installers/macos/onboard.command` (collapse to `keld-agent install --code`)
- Create: `scripts/test-install-sh.sh` (file:// integration test asserting the composed call)

**Interfaces:**
- Consumes: `keld-agent install --code <CODE>` from Task 1.
- Produces: nothing downstream in this repo (installer entrypoints).

- [ ] **Step 1: Write the failing install.sh integration test** `scripts/test-install-sh.sh`.
  It builds a tarball of *fake* `keld`/`keld-agent` that record their args, serves it via `file://`, runs `install.sh --code TESTCODE`, and asserts the fake `keld-agent` was invoked with `install --code TESTCODE`.

```bash
#!/usr/bin/env bash
# Integration test for scripts/install.sh: fake binaries + file:// download, assert the
# code flows into `keld-agent install --code`. No network. Run: bash scripts/test-install-sh.sh
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

# fake binaries: record args to $KELD_TEST_LOG, then exit 0
mkdir -p "$tmp/pkg"
cat > "$tmp/pkg/keld" <<'EOF'
#!/bin/sh
echo "keld $*" >> "$KELD_TEST_LOG"
EOF
cat > "$tmp/pkg/keld-agent" <<'EOF'
#!/bin/sh
echo "keld-agent $*" >> "$KELD_TEST_LOG"
EOF
chmod +x "$tmp/pkg/keld" "$tmp/pkg/keld-agent"

# tarball at <base>/<tag>/keld_linux_amd64.tar.gz (install.sh's expected layout)
mkdir -p "$tmp/dl/testtag"
tar -C "$tmp/pkg" -czf "$tmp/dl/testtag/keld_linux_amd64.tar.gz" keld keld-agent

export KELD_TEST_LOG="$tmp/log"; : > "$KELD_TEST_LOG"
KELD_RELEASE_TAG=testtag \
KELD_DOWNLOAD_BASE="file://$tmp/dl" \
KELD_INSTALL_DIR="$tmp/bin" \
KELD_NO_SIDECAR=1 \
  sh "$here/scripts/install.sh" --code TESTCODE >/dev/null 2>&1 || true

# On x86_64 linux the archive matches; on other hosts uname differs — skip cleanly.
if ! grep -q "keld_linux_amd64" "$tmp/dl/testtag/keld_linux_amd64.tar.gz" 2>/dev/null; then :; fi
if [ "$(uname -s)" != "Linux" ] || { [ "$(uname -m)" != "x86_64" ] && [ "$(uname -m)" != "amd64" ]; }; then
  echo "SKIP: install.sh test requires linux/amd64 host"; exit 0
fi

if ! grep -q "^keld-agent install --code TESTCODE$" "$KELD_TEST_LOG"; then
  echo "FAIL: keld-agent not invoked with the code. Log:"; cat "$KELD_TEST_LOG"; exit 1
fi
echo "PASS: keld-agent install --code TESTCODE"

# env-var precedence: --code wins over KELD_SETUP_CODE
: > "$KELD_TEST_LOG"
KELD_RELEASE_TAG=testtag KELD_DOWNLOAD_BASE="file://$tmp/dl" KELD_INSTALL_DIR="$tmp/bin" \
KELD_NO_SIDECAR=1 KELD_SETUP_CODE=ENVCODE \
  sh "$here/scripts/install.sh" --code ARGCODE >/dev/null 2>&1 || true
if ! grep -q "^keld-agent install --code ARGCODE$" "$KELD_TEST_LOG"; then
  echo "FAIL: --code did not win over KELD_SETUP_CODE. Log:"; cat "$KELD_TEST_LOG"; exit 1
fi
echo "PASS: --code wins over KELD_SETUP_CODE"
```

- [ ] **Step 2: Run → fail.**
  Run: `bash scripts/test-install-sh.sh`
  Expected: FAIL — current `install.sh` doesn't parse `--code` and gates `keld-agent install` behind `command -v systemctl` (no `--code` recorded).

- [ ] **Step 3: Implement `scripts/install.sh`.**
  (a) After the `DEST=...` line near the top, add code parsing:

```sh
# ── One-time setup code (pre-authenticated onboarding) ────────────────────────
# Precedence: a `--code <X>` argument (curl … | sh -s -- --code X) wins over the
# KELD_SETUP_CODE env var. The resolved code is handed to `keld-agent install`.
CODE="${KELD_SETUP_CODE:-}"
while [ $# -gt 0 ]; do
  case "$1" in
    --code) shift; CODE="${1:-}"; [ $# -gt 0 ] && shift ;;
    --code=*) CODE="${1#--code=}"; shift ;;
    *) shift ;;
  esac
done
```
  (b) Replace the `keld-agent` block (currently gated on `command -v systemctl`) with an unconditional, code-aware install:

```sh
if [ -f "${DEST}/keld-agent" ]; then
  chmod +x "${DEST}/keld-agent"
  # keld-agent install owns login → signal setup → service (agent last), and the
  # TTY/headless decision. With a setup code it onboards non-interactively.
  if [ -n "$CODE" ]; then
    "${DEST}/keld-agent" install --code "$CODE" \
      || echo "keld: onboarding/agent install did not fully complete — re-run: keld-agent install --code <CODE>" >&2
  else
    "${DEST}/keld-agent" install \
      || echo "keld: agent install did not complete — finish with: keld login && keld signal setup && keld-agent install" >&2
  fi
fi
```
  > Move this block to run **after** the sidecar-fetch block (so ML is provisioned before the agent starts). If the current file has the sidecar block after the agent block, reorder so: download → sidecar fetch → keld-agent install.

  (c) In the closing "Next steps" echo, drop the now-obsolete "2. Run: keld login / 3. Run: keld signal setup" lines (keld-agent install handles them); keep the PATH guidance and the Gatekeeper note. Replace them with a single line noting onboarding ran (or, if `keld-agent` was absent, keep the manual steps):

```sh
echo "Next steps:"
echo "  Ensure ${DEST} is on your PATH:"
echo "       export PATH=\"${DEST}:\${PATH}\""
if [ ! -f "${DEST}/keld-agent" ]; then
  echo "  Then run:  keld login && keld signal setup"
fi
```

- [ ] **Step 4: Run → pass + shellcheck.**
  Run: `bash scripts/test-install-sh.sh && shellcheck scripts/install.sh scripts/test-install-sh.sh`
  Expected: both PASS lines print; shellcheck clean (fix any warnings it flags).

- [ ] **Step 5: Implement `scripts/install.ps1`** (mirror; defensive on `keld-agent.exe`).
  Add a `-Code` param at the top (after `Set-StrictMode`), defaulting to the env var:

```powershell
param(
    [string]$Code = $env:KELD_SETUP_CODE
)
```
  Before the final "Next steps", add:

```powershell
$agent = Join-Path $InstallDir 'keld-agent.exe'
if (Test-Path $agent) {
    # keld-agent install owns login -> signal setup -> service (agent last).
    if ($Code) {
        & $agent install --code $Code
    } else {
        & $agent install
    }
}
```
  In the "Next steps" block, guard the `keld login` / `keld signal setup` lines behind `if (-not (Test-Path $agent)) { ... }` so they only print when the agent (and thus onboarding) is absent.

- [ ] **Step 6: Lint install.ps1 if a linter is available (best-effort; else note).**
  Run: `command -v pwsh >/dev/null && pwsh -NoProfile -Command "Invoke-ScriptAnalyzer -Path scripts/install.ps1 -Severity Warning,Error" || echo "pwsh/PSScriptAnalyzer not present — install.ps1 verified on Windows CI/manually (no PowerShell in the Linux dev env)"`
  Expected: no Error/Warning findings, or the honest skip note. (Windows behavior is human-verified on CI, matching the prior installer work.)

- [ ] **Step 7: Implement `installers/macos/onboard.command`** — collapse to a single code-aware call.
  Replace lines 10-16 (the login/setup/agent chain) with:

```bash
if [ -n "$CODE" ]; then
  # keld-agent install redeems the code (keld login --code), configures tools, then
  # starts the agent. Fall back to interactive install (browser login) if the code fails.
  "$AGENT" install --code "$CODE" || { echo "Setup code didn't work; falling back to browser login…"; "$AGENT" install || exit 1; }
else
  "$AGENT" install || exit 1
fi
```
  (The `KELD`/`AGENT` path vars stay; `KELD` is now unused — remove the `KELD=...` assignment on line 6, keeping only `AGENT="/usr/local/bin/keld-agent"`.)

- [ ] **Step 8: shellcheck onboard.command.**
  Run: `shellcheck installers/macos/onboard.command`
  Expected: clean.

- [ ] **Step 9: Commit.**

```bash
git add scripts/install.sh scripts/install.ps1 scripts/test-install-sh.sh installers/macos/onboard.command
git commit -m "feat(installers): pass the setup code through to keld-agent install

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

- **Spec coverage:** code-aware `runInstall` + `--code`/env + `--yes`/`--api-url`/`--json` (T1) ✓; stdout TTY fix (T1) ✓; `install.sh`/`install.ps1`/`onboard.command` call `keld-agent install [--code]` (T2) ✓; agent-last preserved (`service.Install` after login/setup) ✓; login-error aborts before service (T1 test) ✓.
- **Placeholders:** none — every step has concrete code/commands; the ps1 lint step degrades honestly when no linter is present.
- **Type consistency:** `installConfig{code,apiURL,yes,jsonOut}` and the widened `runInstall(cfg, isTTY, resolveKeld, run, installService)` are used identically in the command wiring and every test; arg-vector order (`--api-url`, `--json`, then `--code`/`--yes`) matches the test `want` strings.
- **Out of scope (documented):** Windows `.pkg`/Inno GUI wizard; adding a Windows sidecar fetch to install.ps1 (Windows sidecar isn't published — the agent runs deterministic there); fleet/org join-keys.
