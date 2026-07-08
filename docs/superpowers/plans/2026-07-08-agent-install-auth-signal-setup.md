# `keld-agent install` runs CLI auth + signal setup — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `keld-agent install` set the user up end-to-end — run `keld login`, then `keld signal setup`, then register the OS service — instead of only registering the service.

**Architecture:** The `install` cobra command's `RunE` delegates to a thin, injectable `runInstall` orchestrator. It resolves the `keld` binary (beside the `keld-agent` executable, then `PATH`), shells out to `keld login` and `keld signal setup` with inherited stdio, and calls `service.Install()` last. Two seams — a dir-parameterized resolver helper and a `stepRunner` function value — make the sequence unit-testable without spawning subprocesses or touching the OS service.

**Tech Stack:** Go, cobra, `os/exec`. Standard library `testing` (no external test deps).

## Global Constraints

- CLI is a single static Go binary, no runtime deps (the `keld` binary dependency is a sibling executable the installers already place, not a linked dep).
- Service install (`service.Install()`) must run **last**, after login + setup, so the daemon comes up already configured (it refuses to run without `~/.keld/hook.json`).
- Both steps run every install — no `--no-login` / `--skip-setup` flags, no skip-if-already-done checks.
- Abort the install on the first step failure.
- Do not modify `internal/cli` (login/setup internals), the shell scripts, or the pkg installers.
- End commit messages with the repo's `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>` trailer.

---

### Task 1: `keld` binary resolver

**Files:**
- Modify: `internal/agentcli/agentcli.go`
- Test: `internal/agentcli/agentcli_test.go` (create)

**Interfaces:**
- Consumes: nothing from other tasks.
- Produces:
  - `keldName() string` — `"keld.exe"` on Windows, else `"keld"`.
  - `keldInDir(dir string) (string, bool)` — path to a regular-file `keld` in `dir`, and whether found.
  - `resolveKeld() (string, error)` — resolves `keld` beside `os.Executable()`, then `exec.LookPath`; error if not found.

- [ ] **Step 1: Write the failing test**

Add to `internal/agentcli/agentcli_test.go`:

```go
package agentcli

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestKeldInDir(t *testing.T) {
	dir := t.TempDir()

	if _, ok := keldInDir(dir); ok {
		t.Fatal("expected keld not found in empty dir")
	}

	bin := filepath.Join(dir, keldName())
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := keldInDir(dir)
	if !ok {
		t.Fatal("expected keld found after creating it")
	}
	if got != bin {
		t.Fatalf("got %q, want %q", got, bin)
	}

	if runtime.GOOS == "windows" && keldName() != "keld.exe" {
		t.Fatalf("windows keldName = %q, want keld.exe", keldName())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agentcli/ -run TestKeldInDir -v`
Expected: FAIL to compile — `undefined: keldInDir` / `undefined: keldName`.

- [ ] **Step 3: Write minimal implementation**

Add these to `internal/agentcli/agentcli.go`. Extend the import block with `os/exec`, `path/filepath`, and `runtime` (keep the existing imports):

```go
// keldName is the platform basename of the keld CLI binary.
func keldName() string {
	if runtime.GOOS == "windows" {
		return "keld.exe"
	}
	return "keld"
}

// isRegularFile reports whether p exists and is a regular file.
func isRegularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// keldInDir returns the path to a regular-file keld binary in dir, if present.
func keldInDir(dir string) (string, bool) {
	p := filepath.Join(dir, keldName())
	if isRegularFile(p) {
		return p, true
	}
	return "", false
}

// resolveKeld locates the keld CLI binary: first beside the running keld-agent
// executable (how the installers lay it out), then on PATH.
func resolveKeld() (string, error) {
	if exe, err := os.Executable(); err == nil {
		if p, ok := keldInDir(filepath.Dir(exe)); ok {
			return p, nil
		}
	}
	if p, err := exec.LookPath(keldName()); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("keld binary not found beside keld-agent or on PATH; install keld first")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agentcli/ -run TestKeldInDir -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/agentcli/agentcli.go internal/agentcli/agentcli_test.go
git commit -m "feat(agent): add keld CLI binary resolver for installer

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

### Task 2: `runInstall` orchestration + command wiring

**Files:**
- Modify: `internal/agentcli/agentcli.go`
- Test: `internal/agentcli/agentcli_test.go`

**Interfaces:**
- Consumes: `resolveKeld() (string, error)` (Task 1).
- Produces:
  - `type stepRunner func(name string, args ...string) error`
  - `runStep(name string, args ...string) error` — production runner: `exec.Command` with `os.Stdin/Stdout/Stderr` inherited.
  - `runInstall(resolveKeld func() (string, error), run stepRunner, installService func() error) error` — resolves keld, runs `keld login`, then `keld signal setup`, then `installService()`; aborts on first error.

- [ ] **Step 1: Write the failing test**

Add to `internal/agentcli/agentcli_test.go` (imports: add `"errors"` and `"strings"` to the test import block):

```go
func TestRunInstallSequence(t *testing.T) {
	var calls []string
	resolve := func() (string, error) { return "/fake/keld", nil }
	run := func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	installed := false
	install := func() error { installed = true; return nil }

	if err := runInstall(resolve, run, install); err != nil {
		t.Fatalf("runInstall: %v", err)
	}

	want := []string{"/fake/keld login", "/fake/keld signal setup"}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("steps = %v, want %v", calls, want)
	}
	if !installed {
		t.Fatal("service install was not called")
	}
}

func TestRunInstallAbortsOnLoginFailure(t *testing.T) {
	var calls []string
	resolve := func() (string, error) { return "/fake/keld", nil }
	run := func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		if strings.HasSuffix(calls[len(calls)-1], "login") {
			return errors.New("boom")
		}
		return nil
	}
	installed := false
	install := func() error { installed = true; return nil }

	if err := runInstall(resolve, run, install); err == nil {
		t.Fatal("expected error when login fails")
	}
	if len(calls) != 1 {
		t.Fatalf("expected to stop after login, got calls %v", calls)
	}
	if installed {
		t.Fatal("service install must not run when login fails")
	}
}

func TestRunInstallAbortsWhenKeldMissing(t *testing.T) {
	resolve := func() (string, error) { return "", errors.New("not found") }
	ran := false
	run := func(name string, args ...string) error { ran = true; return nil }
	installed := false
	install := func() error { installed = true; return nil }

	if err := runInstall(resolve, run, install); err == nil {
		t.Fatal("expected error when keld is missing")
	}
	if ran || installed {
		t.Fatal("no steps should run when keld cannot be resolved")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/agentcli/ -run TestRunInstall -v`
Expected: FAIL to compile — `undefined: runInstall`.

- [ ] **Step 3: Write minimal implementation**

Add to `internal/agentcli/agentcli.go`:

```go
// stepRunner runs a keld subcommand. The production implementation execs it
// with the parent's stdio so interactive flows (device auth, config diffs) work.
type stepRunner func(name string, args ...string) error

// runStep is the production stepRunner: run the command with inherited stdio.
func runStep(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// runInstall sets the user up, then registers the service. Order matters: the
// daemon refuses to run until signal setup has written ~/.keld/hook.json, and
// installService starts it immediately — so service install runs last.
func runInstall(resolveKeld func() (string, error), run stepRunner, installService func() error) error {
	keld, err := resolveKeld()
	if err != nil {
		return err
	}
	if err := run(keld, "login"); err != nil {
		return fmt.Errorf("keld login: %w", err)
	}
	if err := run(keld, "signal", "setup"); err != nil {
		return fmt.Errorf("keld signal setup: %w", err)
	}
	return installService()
}
```

Then change the `install` command registration in `NewRootCmd` from:

```go
	root.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Install keld-agent as a per-user autostart service.",
		RunE:  func(cmd *cobra.Command, args []string) error { return service.Install() },
	})
```

to:

```go
	root.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Log in, set up telemetry, and install keld-agent as a per-user autostart service.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstall(resolveKeld, runStep, service.Install)
		},
	})
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/agentcli/ -run TestRunInstall -v`
Expected: PASS (all three tests).

- [ ] **Step 5: Full build + test**

Run: `make build-binaries && go test ./...`
Expected: build succeeds; all tests pass.

- [ ] **Step 6: Commit**

```bash
git add internal/agentcli/agentcli.go internal/agentcli/agentcli_test.go
git commit -m "feat(agent): install runs keld login + signal setup before service

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Self-Review

**Spec coverage:**
- Resolve `keld` beside executable then PATH → Task 1 (`resolveKeld`).
- `keld login` → `keld signal setup` → `service.Install()` last → Task 2 (`runInstall`).
- Shell out with inherited stdio → Task 2 (`runStep`).
- Abort on first failure → Task 2 (`TestRunInstallAbortsOnLoginFailure`).
- Clear error when `keld` absent → Task 1 (`resolveKeld` error) + Task 2 (`TestRunInstallAbortsWhenKeldMissing`).
- Two test seams (dir-parameterized resolver + runner) → Tasks 1 & 2.
- Out-of-scope items (flags, idempotency, scripts, cli internals) → not touched.

**Placeholder scan:** none — every code and command step is concrete.

**Type consistency:** `keldName`/`keldInDir`/`resolveKeld` (Task 1) are consumed by `runInstall` and `resolveKeld` (Task 2) with matching signatures. `stepRunner` signature matches `runStep` and the test fakes. `runInstall`'s parameter order `(resolveKeld, run, installService)` matches every call site and test.
