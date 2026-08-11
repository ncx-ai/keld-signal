//go:build darwin

package service

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// A disabled launchd override makes `launchctl bootstrap` fail (error 5); the
// follow-up `kickstart` then fails with a cryptic exit 113. startJob must clear
// the override with `launchctl enable` before loading the job.
func TestStartJobEnablesBeforeKickstart(t *testing.T) {
	var calls [][]string
	run := func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}

	if err := startJob(run, "gui/501", "/tmp/co.keld.agent.plist"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	enableIdx, kickstartIdx := -1, -1
	for i, c := range calls {
		if len(c) >= 3 && c[0] == "launchctl" && c[1] == "enable" && c[2] == "gui/501/"+Label {
			enableIdx = i
		}
		if len(c) >= 2 && c[0] == "launchctl" && c[1] == "kickstart" {
			kickstartIdx = i
		}
	}
	if enableIdx == -1 {
		t.Fatalf("startJob never called `launchctl enable gui/501/%s`; calls=%v", Label, calls)
	}
	if kickstartIdx == -1 {
		t.Fatalf("startJob never called `launchctl kickstart`; calls=%v", calls)
	}
	if enableIdx > kickstartIdx {
		t.Fatalf("enable (idx %d) must precede kickstart (idx %d); calls=%v", enableIdx, kickstartIdx, calls)
	}
}

// A benign bootstrap failure (job already loaded) must not fail startJob.
func TestStartJobIgnoresBootstrapError(t *testing.T) {
	run := func(name string, args ...string) error {
		if len(args) >= 1 && args[0] == "bootstrap" {
			return errors.New("Bootstrap failed: 37: Operation already in progress")
		}
		return nil
	}
	if err := startJob(run, "gui/501", "/tmp/p.plist"); err != nil {
		t.Fatalf("startJob should ignore a benign bootstrap error, got: %v", err)
	}
}

// A kickstart failure is the real signal the job did not start; surface it
// instead of swallowing it behind exit 0.
func TestStartJobReturnsKickstartError(t *testing.T) {
	sentinel := errors.New("kickstart boom")
	run := func(name string, args ...string) error {
		if len(args) >= 1 && args[0] == "kickstart" {
			return sentinel
		}
		return nil
	}
	err := startJob(run, "gui/501", "/tmp/p.plist")
	if err == nil {
		t.Fatalf("expected kickstart error to surface, got nil")
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("expected wrapped kickstart error, got: %v", err)
	}
}

// Restart must also clear a stale disabled override, and use `kickstart -k` so a
// running daemon is actually killed and restarted (picks up a new binary).
func TestRestartJobEnablesAndForceKickstarts(t *testing.T) {
	var calls [][]string
	run := func(name string, args ...string) error {
		calls = append(calls, append([]string{name}, args...))
		return nil
	}

	if err := restartJob(run, "gui/501", "/tmp/p.plist"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	sawEnable, sawForceKick := false, false
	for _, c := range calls {
		if len(c) >= 3 && c[0] == "launchctl" && c[1] == "enable" && c[2] == "gui/501/"+Label {
			sawEnable = true
		}
		if len(c) >= 4 && c[0] == "launchctl" && c[1] == "kickstart" && c[2] == "-k" && c[3] == "gui/501/"+Label {
			sawForceKick = true
		}
	}
	if !sawEnable {
		t.Fatalf("restartJob never enabled the job; calls=%v", calls)
	}
	if !sawForceKick {
		t.Fatalf("restartJob never force-kickstarted (`kickstart -k`); calls=%v", calls)
	}
}

func TestRestartJobReturnsKickstartError(t *testing.T) {
	sentinel := errors.New("restart boom")
	run := func(name string, args ...string) error {
		if len(args) >= 1 && args[0] == "kickstart" {
			return sentinel
		}
		return nil
	}
	if err := restartJob(run, "gui/501", "/tmp/p.plist"); !errors.Is(err, sentinel) {
		t.Fatalf("expected wrapped kickstart error, got: %v", err)
	}
}

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
	wrote, err := syncPlist(p, logDir, "NEW-PLIST", writeFile,
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
