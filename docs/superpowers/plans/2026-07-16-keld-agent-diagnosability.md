# keld-agent Diagnosability Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a misconfigured/crashing `keld-agent` diagnosable — print swallowed errors to stderr, and give the macOS launchd job a file to log to.

**Architecture:** Two independent fixes. Fix 1 prints any error returned from the cobra command tree to stderr in `agentcli.Execute` (the error is currently silenced). Fix 2 adds `StandardOutPath`/`StandardErrorPath` to the macOS LaunchAgent plist, writes logs under `~/.keld/logs/`, and makes `Start()`/`Restart()` rewrite a stale plist so already-installed agents pick up the log paths without a reinstall.

**Tech Stack:** Go 1.26, cobra CLI, launchd (macOS). No new dependencies.

## Global Constraints

- **Go 1.26** toolchain required (`go.mod` declares `go 1.26`). Not installed on the target machine — install before executing.
- **Module path:** `github.com/ncx-ai/keld-signal`.
- **Fix 2 is darwin-only.** Linux (systemd/journald) already captures output; Windows (logon task) is an explicit out-of-scope follow-up.
- **Keep `SilenceUsage: true` and `SilenceErrors: true`** on the root command — Fix 1 prints the error itself; it must not re-enable cobra's usage dump.
- **Log paths are absolute** — launchd does not expand `~`; use the `paths` helpers which resolve the real home.
- **No log rotation** — deliberately out of scope (documented risk in the spec).
- **No new external dependencies.**
- Work happens on branch `fix/daemon-diagnosability` (spec already committed there).

## File Structure

- `internal/paths/paths.go` — add `AgentLogDir`/`AgentStdoutLog`/`AgentStderrLog` helpers (Task 1).
- `internal/paths/paths_test.go` — test for the new helpers (Task 1).
- `internal/agentcli/agentcli.go` — testable `executeCmd` seam + `Execute` prints errors (Task 2).
- `internal/agentcli/agentcli_test.go` — tests for the seam (Task 2).
- `internal/agent/service/service.go` — `LaunchAgentPlist` gains log-path params (Task 3).
- `internal/agent/service/service_test.go` — update existing plist test (Task 3).
- `internal/agent/service/service_darwin.go` — `Install` passes log paths; `Start`/`Restart` sync a stale plist (Tasks 3 & 4).
- `internal/agent/service/service_darwin_test.go` — new darwin-tagged test for `syncPlist` (Task 4).

---

### Task 1: paths log helpers

**Files:**
- Modify: `internal/paths/paths.go` (after line 39, beside `DebugLogPath`)
- Test: `internal/paths/paths_test.go`

**Interfaces:**
- Consumes: `KeldHome() string` (existing).
- Produces: `AgentLogDir() string` → `~/.keld/logs`; `AgentStdoutLog() string` → `~/.keld/logs/agent.out.log`; `AgentStderrLog() string` → `~/.keld/logs/agent.err.log`.

- [ ] **Step 1: Write the failing test**

Add to `internal/paths/paths_test.go`:

```go
func TestAgentLogPaths(t *testing.T) {
	t.Setenv("KELD_HOME", "/tmp/kh")
	if AgentLogDir() != filepath.Join("/tmp/kh", "logs") {
		t.Fatalf("log dir %q", AgentLogDir())
	}
	if AgentStdoutLog() != filepath.Join("/tmp/kh", "logs", "agent.out.log") {
		t.Fatalf("stdout %q", AgentStdoutLog())
	}
	if AgentStderrLog() != filepath.Join("/tmp/kh", "logs", "agent.err.log") {
		t.Fatalf("stderr %q", AgentStderrLog())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/paths/ -run TestAgentLogPaths -v`
Expected: FAIL to compile — `undefined: AgentLogDir` (and the other two).

- [ ] **Step 3: Write minimal implementation**

In `internal/paths/paths.go`, add after the `DebugLogPath` line (line 39):

```go
func AgentLogDir() string    { return filepath.Join(KeldHome(), "logs") }
func AgentStdoutLog() string { return filepath.Join(AgentLogDir(), "agent.out.log") }
func AgentStderrLog() string { return filepath.Join(AgentLogDir(), "agent.err.log") }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/paths/ -run TestAgentLogPaths -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/paths/paths.go internal/paths/paths_test.go
git commit -m "feat(paths): add agent log dir/file helpers under ~/.keld/logs"
```

---

### Task 2: print returned errors to stderr (Fix 1)

**Files:**
- Modify: `internal/agentcli/agentcli.go:265-271` (the `Execute` function) and its import block (add `io`)
- Test: `internal/agentcli/agentcli_test.go` (add `cobra` to imports)

**Interfaces:**
- Consumes: `NewRootCmd() *cobra.Command` (existing).
- Produces: `executeCmd(root *cobra.Command, stderr io.Writer) int` (internal seam); `Execute() int` (unchanged signature, now delegates to `executeCmd`).

- [ ] **Step 1: Write the failing test**

Add to `internal/agentcli/agentcli_test.go` (add `"github.com/spf13/cobra"` to the import block; `bytes`, `errors`, `strings`, `testing` are already imported):

```go
func TestExecuteCmdPrintsErrorToStderr(t *testing.T) {
	root := &cobra.Command{
		Use:           "x",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(*cobra.Command, []string) error { return errors.New("boom") },
	}
	var buf bytes.Buffer
	code := executeCmd(root, &buf)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Fatalf("stderr = %q, want to contain \"boom\"", buf.String())
	}
}

func TestExecuteCmdSuccessIsSilent(t *testing.T) {
	root := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
	var buf bytes.Buffer
	code := executeCmd(root, &buf)
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if buf.String() != "" {
		t.Fatalf("stderr = %q, want empty", buf.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agentcli/ -run TestExecuteCmd -v`
Expected: FAIL to compile — `undefined: executeCmd`.

- [ ] **Step 3: Write minimal implementation**

In `internal/agentcli/agentcli.go`, add `"io"` to the import block, then replace the existing `Execute` function (lines 265-271):

```go
// executeCmd runs root and, on error, prints it to stderr (once) before
// returning exit code 1. The root command keeps SilenceErrors/SilenceUsage so
// cobra prints neither the error nor usage; printing here is the single place a
// returned error becomes visible. Without this, a daemon.Run failure (e.g. an
// unconfigured agent) exits 1 with completely empty output.
func executeCmd(root *cobra.Command, stderr io.Writer) int {
	if err := root.Execute(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// Execute runs the keld-agent CLI and returns an exit code.
func Execute() int { return executeCmd(NewRootCmd(), os.Stderr) }
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agentcli/ -run TestExecuteCmd -v`
Expected: PASS (both tests).

- [ ] **Step 5: Commit**

```bash
git add internal/agentcli/agentcli.go internal/agentcli/agentcli_test.go
git commit -m "fix(agentcli): print returned errors to stderr instead of silent exit 1"
```

---

### Task 3: plist log-path keys + Install (Fix 2, part 1)

**Files:**
- Modify: `internal/agent/service/service.go:9-24` (`LaunchAgentPlist`)
- Modify: `internal/agent/service/service_test.go:8-19` (`TestLaunchAgentPlistContainsExecAndLabel`)
- Modify: `internal/agent/service/service_darwin.go:17-34` (`Install`) and its import block (add `paths`)

**Interfaces:**
- Consumes: `paths.AgentLogDir()`, `paths.AgentStdoutLog()`, `paths.AgentStderrLog()` (Task 1).
- Produces: `LaunchAgentPlist(execPath, stdoutPath, stderrPath string) string` (signature changed from one arg to three).

- [ ] **Step 1: Update the failing test**

Replace `TestLaunchAgentPlistContainsExecAndLabel` in `internal/agent/service/service_test.go`:

```go
func TestLaunchAgentPlistContainsExecAndLabel(t *testing.T) {
	p := LaunchAgentPlist(
		"/usr/local/bin/keld-agent",
		"/home/u/.keld/logs/agent.out.log",
		"/home/u/.keld/logs/agent.err.log",
	)
	if !strings.Contains(p, "<string>/usr/local/bin/keld-agent</string>") {
		t.Fatalf("plist missing exec path:\n%s", p)
	}
	if !strings.Contains(p, "co.keld.agent") {
		t.Fatalf("plist missing label:\n%s", p)
	}
	if !strings.Contains(p, "<key>RunAtLoad</key>") {
		t.Fatalf("plist missing RunAtLoad:\n%s", p)
	}
	if !strings.Contains(p, "<key>StandardOutPath</key><string>/home/u/.keld/logs/agent.out.log</string>") {
		t.Fatalf("plist missing StandardOutPath:\n%s", p)
	}
	if !strings.Contains(p, "<key>StandardErrorPath</key><string>/home/u/.keld/logs/agent.err.log</string>") {
		t.Fatalf("plist missing StandardErrorPath:\n%s", p)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/service/ -run TestLaunchAgentPlist -v`
Expected: FAIL to compile — `too many arguments in call to LaunchAgentPlist`.

- [ ] **Step 3: Write minimal implementation**

In `internal/agent/service/service.go`, replace `LaunchAgentPlist`:

```go
// LaunchAgentPlist returns the macOS LaunchAgent plist for the given exec path,
// with launchd stdout/stderr redirected to stdoutPath/stderrPath (absolute
// paths — launchd does not expand "~"). Without these, a crash-looping daemon
// logs nowhere.
func LaunchAgentPlist(execPath, stdoutPath, stderrPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, Label, execPath, stdoutPath, stderrPath)
}
```

Then in `internal/agent/service/service_darwin.go`, add `"github.com/ncx-ai/keld-signal/internal/paths"` to the import block and replace `Install`:

```go
func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	p := plistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.AgentLogDir(), 0o755); err != nil {
		return err
	}
	plist := LaunchAgentPlist(exe, paths.AgentStdoutLog(), paths.AgentStderrLog())
	if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
		return err
	}
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	// bootout then bootstrap = a restart, so a REINSTALL over a running agent picks up
	// the newly-installed binary (launchd starts whatever is at the plist's program path).
	_ = exec.Command("launchctl", "bootout", uid, p).Run() // ignore if not loaded
	return exec.Command("launchctl", "bootstrap", uid, p).Run()
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/service/ -run TestLaunchAgentPlist -v`
Expected: PASS.

Also confirm the package still builds on darwin:
Run: `go build ./internal/agent/service/`
Expected: no output, exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/service/service.go internal/agent/service/service_test.go internal/agent/service/service_darwin.go
git commit -m "feat(service): write launchd stdout/stderr to ~/.keld/logs"
```

---

### Task 4: rewrite stale plist on Start/Restart (Fix 2, part 2)

**Files:**
- Modify: `internal/agent/service/service_darwin.go:43-59` (`Start`, `Restart`) — add `syncPlist`, `currentPlist`, `reloadJob` helpers
- Test: `internal/agent/service/service_darwin_test.go` (new, darwin-tagged)

**Interfaces:**
- Consumes: `LaunchAgentPlist(execPath, stdoutPath, stderrPath string)` (Task 3); `paths.AgentLogDir()`, `paths.AgentStdoutLog()`, `paths.AgentStderrLog()` (Task 1); `plistPath()`, `Label` (existing).
- Produces (internal, darwin): `syncPlist(path, logDir, want string, write func(string, []byte) error, reload func() error) (bool, error)`; `currentPlist() (string, error)`; `reloadJob() error`.

- [ ] **Step 1: Write the failing test**

Create `internal/agent/service/service_darwin_test.go`:

```go
//go:build darwin

package service

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSyncPlistNoRewriteWhenCurrent(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "co.keld.agent.plist")
	want := "PLIST-CONTENT"
	if err := os.WriteFile(p, []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	reloaded := false
	wrote, err := syncPlist(p, filepath.Join(dir, "logs"), want,
		func(string, []byte) error { t.Fatal("write must not be called when current"); return nil },
		func() error { reloaded = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if wrote {
		t.Fatal("wrote = true, want false (plist already current)")
	}
	if reloaded {
		t.Fatal("reload must not be called when current")
	}
}

func TestSyncPlistRewritesWhenStale(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "co.keld.agent.plist")
	if err := os.WriteFile(p, []byte("OLD-PLIST"), 0o644); err != nil {
		t.Fatal(err)
	}
	logDir := filepath.Join(dir, "logs")
	reloaded := false
	wrote, err := syncPlist(p, logDir, "NEW-PLIST", os.WriteFile,
		func() error { reloaded = true; return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !wrote {
		t.Fatal("wrote = false, want true (plist was stale)")
	}
	if !reloaded {
		t.Fatal("reload not called after rewrite")
	}
	got, _ := os.ReadFile(p)
	if string(got) != "NEW-PLIST" {
		t.Fatalf("plist = %q, want NEW-PLIST", got)
	}
	if fi, err := os.Stat(logDir); err != nil || !fi.IsDir() {
		t.Fatalf("log dir not created: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agent/service/ -run TestSyncPlist -v`
Expected: FAIL to compile — `undefined: syncPlist`.

- [ ] **Step 3: Write minimal implementation**

In `internal/agent/service/service_darwin.go`, add the three helpers and replace `Start`/`Restart`:

```go
// syncPlist ensures the plist at path equals want. If it already matches,
// nothing happens (returns false). Otherwise — differing, missing, or
// unreadable — it creates the plist and log directories, writes want, and
// reloads the job via reload (returns true). write/reload are seams; production
// wires os.WriteFile and reloadJob (a launchctl bootout+bootstrap).
func syncPlist(path, logDir, want string, write func(string, []byte) error, reload func() error) (bool, error) {
	if cur, err := os.ReadFile(path); err == nil && string(cur) == want {
		return false, nil
	}
	for _, d := range []string{filepath.Dir(path), logDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return false, err
		}
	}
	if err := write(path, []byte(want)); err != nil {
		return false, err
	}
	return true, reload()
}

// currentPlist is the plist this binary should be installed with.
func currentPlist() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return LaunchAgentPlist(exe, paths.AgentStdoutLog(), paths.AgentStderrLog()), nil
}

// reloadJob adopts an on-disk plist change: bootout then bootstrap.
func reloadJob() error {
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	p := plistPath()
	_ = exec.Command("launchctl", "bootout", uid, p).Run() // ignore if not loaded
	return exec.Command("launchctl", "bootstrap", uid, p).Run()
}

// Start loads the agent if needed, then (re)starts the job. It first syncs a
// stale plist so an agent installed before log paths existed adopts them.
func Start() error {
	want, err := currentPlist()
	if err != nil {
		return err
	}
	if _, err := syncPlist(plistPath(), paths.AgentLogDir(), want, os.WriteFile, reloadJob); err != nil {
		return err
	}
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootstrap", uid, plistPath()).Run() // no-op if already loaded
	return exec.Command("launchctl", "kickstart", uid+"/"+Label).Run()
}

// Stop unloads the agent (KeepAlive would otherwise respawn it); Install/Start reload it.
func Stop() error {
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	return exec.Command("launchctl", "bootout", uid, plistPath()).Run()
}

// Restart kills and restarts the running job (picks up a newly-installed
// binary). It first syncs a stale plist so log paths are adopted.
func Restart() error {
	want, err := currentPlist()
	if err != nil {
		return err
	}
	if _, err := syncPlist(plistPath(), paths.AgentLogDir(), want, os.WriteFile, reloadJob); err != nil {
		return err
	}
	return exec.Command("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", os.Getuid(), Label)).Run()
}
```

Note: `Stop` is shown unchanged for context (it sits between `Start` and `Restart`); leave it as-is if your edit tool targets `Start`/`Restart` individually.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agent/service/ -run TestSyncPlist -v`
Expected: PASS (both tests).

Confirm the package builds:
Run: `go build ./internal/agent/service/`
Expected: no output, exit 0.

- [ ] **Step 5: Commit**

```bash
git add internal/agent/service/service_darwin.go internal/agent/service/service_darwin_test.go
git commit -m "fix(service): rewrite stale launchd plist on start/restart so existing installs get log paths"
```

---

### Task 5: full-suite check + end-to-end verification on macOS

**Files:** none (verification only).

**Interfaces:** exercises the real binary and launchd.

- [ ] **Step 1: Format, vet, and run the whole suite**

Run:
```bash
gofmt -l internal/    # expect: no output (all formatted)
go vet ./...          # expect: no output, exit 0
go test ./...         # expect: all packages ok/PASS
```
Expected: clean. Fix anything that fails before proceeding.

- [ ] **Step 2: Build the binaries**

Run: `make build-binaries DEST=$HOME/.local/bin` (or `go build -o ~/.local/bin/keld-agent ./cmd/keld-agent`)
Expected: `~/.local/bin/keld-agent` rebuilt from this branch.

- [ ] **Step 3: Reproduce the original silent failure, now fixed**

```bash
mv ~/.keld/hook.json ~/.keld/hook.json.bak
~/.local/bin/keld-agent run; echo "exit=$?"
```
Expected: prints `keld-agent: not configured (run \`keld login\` / setup first)` to stderr and `exit=1` (previously: empty output, exit 1).

- [ ] **Step 4: Verify plist rewrite + log capture**

```bash
~/.local/bin/keld-agent restart
grep -c 'StandardErrorPath' ~/Library/LaunchAgents/co.keld.agent.plist   # expect: 1
sleep 2
cat ~/.keld/logs/agent.err.log                                          # expect: the "not configured" message
```
Expected: plist now contains the log-path keys, and `~/.keld/logs/agent.err.log` contains the not-configured message (launchd captured it because the job is flapping while unconfigured).

- [ ] **Step 5: Restore config and confirm healthy**

```bash
mv ~/.keld/hook.json.bak ~/.keld/hook.json
~/.local/bin/keld-agent restart
sleep 2
tail -n 5 ~/.keld/logs/agent.out.log    # expect: "listening on 127.0.0.1:<port>", no crash loop
~/.local/bin/keld-agent status | grep -E 'state =|last exit code'
```
Expected: `state = running`, `last exit code = 0`, and the out-log shows the listening line. No further flapping.

- [ ] **Step 6: Push the branch and open the PR**

```bash
git push -u origin fix/daemon-diagnosability
/opt/homebrew/bin/gh pr create --fill --title "Make keld-agent daemon failures diagnosable"
```
Expected: PR created. Reference the two drafted issues (silent exit-1; plist has no log paths) in the PR body if they were filed.

---

## Self-Review

**Spec coverage:**
- Fix 1 (print error in `Execute`) → Task 2. ✔
- Fix 1 unconfigured exits with clear message → existing `daemon.Run` behavior, now visible (Task 2); verified in Task 5 Step 3. ✔
- Fix 2a paths helpers (`~/.keld/logs/`) → Task 1. ✔
- Fix 2b plist log keys + `Install` mkdir → Task 3. ✔
- Fix 2c rewrite on Start/Restart → Task 4. ✔
- Scope: darwin-only, no rotation → Global Constraints; verification is darwin (Task 5). ✔
- Testing (agentcli / service / paths) → Tasks 1–4; full suite + manual verify → Task 5. ✔

**Placeholder scan:** none — every code step has complete code; every command has expected output.

**Type consistency:** `LaunchAgentPlist(execPath, stdoutPath, stderrPath string)` is defined in Task 3 and consumed with three args in Task 3's test and Task 4's `currentPlist`. `syncPlist`/`currentPlist`/`reloadJob` signatures in Task 4's Interfaces match the implementation. `executeCmd(root, stderr)` in Task 2 matches its test. `paths.AgentLogDir/AgentStdoutLog/AgentStderrLog` names are consistent across Tasks 1, 3, 4.
