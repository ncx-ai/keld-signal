package update

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// staged lays out a machine mid-update: the new bytes are in place, the old
// ones are parked at .prev, and the marker says it is unproven.
func staged(t *testing.T, attemptedAt time.Time) (dir, statePath string) {
	t.Helper()
	dir = t.TempDir()
	write(t, filepath.Join(dir, "keld-agent"), "agent-v2")
	write(t, filepath.Join(dir, "keld-agent.prev"), "agent-v1")
	write(t, filepath.Join(dir, "keld"), "keld-v2")
	write(t, filepath.Join(dir, "keld.prev"), "keld-v1")
	statePath = filepath.Join(t.TempDir(), "state.json")
	if err := SaveState(statePath, State{
		From: "v1", To: "v2", InstallDir: dir,
		Prev:           []string{filepath.Join(dir, "keld-agent.prev"), filepath.Join(dir, "keld.prev")},
		PendingConfirm: true, AttemptedAt: attemptedAt,
	}); err != nil {
		t.Fatal(err)
	}
	return dir, statePath
}

func TestConfirmIsANoOpWithNoMarker(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	r := &fakeRestart{}
	rec := &recorded{}
	if err := Confirm(p, "v1", now, 15*time.Minute, r, rec.emit); err != nil {
		t.Fatal(err)
	}
	if r.count() != 0 || len(rec.events) != 0 {
		t.Fatalf("restarts=%d events=%v", r.count(), rec.events)
	}
}

// The new version came up. Clear the marker, drop the parked copies, say so.
func TestConfirmAcceptsTheNewVersion(t *testing.T) {
	dir, p := staged(t, now)
	r := &fakeRestart{}
	rec := &recorded{}
	if err := Confirm(p, "v2", now, 15*time.Minute, r, rec.emit); err != nil {
		t.Fatal(err)
	}
	s, _ := LoadState(p)
	if s.PendingConfirm {
		t.Fatal("marker not cleared")
	}
	if s.LastOutcome != "applied" {
		t.Fatalf("outcome = %q", s.LastOutcome)
	}
	if _, err := os.Stat(filepath.Join(dir, "keld-agent.prev")); !os.IsNotExist(err) {
		t.Fatal(".prev copies should be dropped once the version is proven")
	}
	if !rec.has("update.applied") {
		t.Fatalf("events = %v", rec.events)
	}
	if r.count() != 0 {
		t.Fatal("a successful confirm must not restart")
	}
}

// The restart did not take the new binary — we are still the old version. Put
// the old bytes back, remember the failure, restart.
func TestConfirmRollsBackWhenTheOldVersionIsStillRunning(t *testing.T) {
	dir, p := staged(t, now)
	r := &fakeRestart{}
	rec := &recorded{}
	if err := Confirm(p, "v1", now, 15*time.Minute, r, rec.emit); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dir, "keld-agent")) != "agent-v1" {
		t.Fatalf("not restored: %q", read(t, filepath.Join(dir, "keld-agent")))
	}
	if read(t, filepath.Join(dir, "keld")) != "keld-v1" {
		t.Fatal("cli not restored")
	}
	s, _ := LoadState(p)
	if s.PendingConfirm {
		t.Fatal("marker not cleared")
	}
	if !s.HasFailed("v2") {
		t.Fatal("the failed version must be remembered, or the next poll re-applies it forever")
	}
	if s.LastOutcome != "rolled_back" {
		t.Fatalf("outcome = %q", s.LastOutcome)
	}
	if r.count() != 1 {
		t.Fatalf("restarts = %d, want 1", r.count())
	}
	if !rec.has("update.rolled_back") {
		t.Fatalf("events = %v", rec.events)
	}
}

// The new binary started but never reached a healthy state — it crashed before
// it could clear the marker. Whichever binary comes up next sees a STALE
// pending marker and undoes the swap. This is the case that makes a bad
// release self-healing rather than a bricked fleet.
func TestConfirmRollsBackAStaleMarkerEvenAsTheNewVersion(t *testing.T) {
	dir, p := staged(t, now.Add(-30*time.Minute))
	r := &fakeRestart{}
	rec := &recorded{}
	if err := Confirm(p, "v2", now, 15*time.Minute, r, rec.emit); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(dir, "keld-agent")) != "agent-v1" {
		t.Fatal("a stale marker must roll back regardless of which version is running")
	}
	s, _ := LoadState(p)
	if !s.HasFailed("v2") {
		t.Fatal("failure not recorded")
	}
}

// A version that rolled back must be refused by the very next decision, or the
// machine loops: swap, crash, roll back, swap.
func TestARolledBackVersionIsRefusedByTheNextDecision(t *testing.T) {
	_, p := staged(t, now)
	r := &fakeRestart{}
	rec := &recorded{}
	if err := Confirm(p, "v1", now, 15*time.Minute, r, rec.emit); err != nil {
		t.Fatal(err)
	}
	s, _ := LoadState(p)
	d := Decide("v1", Target{Version: "v2", Enabled: true}, s, now.Add(48*time.Hour), false, time.Hour)
	if d.Act || d.Reason != ReasonFailedPreviously {
		t.Fatalf("got %+v", d)
	}
	// But the moment Atlas moves the pin, the machine is free again.
	d2 := Decide("v1", Target{Version: "v3", Enabled: true}, s, now.Add(48*time.Hour), false, time.Hour)
	if !d2.Act {
		t.Fatalf("a moved pin must be actionable: %+v", d2)
	}
}

// Restore failed: the .prev copies are gone. This must NOT restart — the
// machine is in an unknown state and bouncing the service cannot improve it.
func TestConfirmDoesNotRestartWhenRestoreFails(t *testing.T) {
	dir, p := staged(t, now)
	if err := os.Remove(filepath.Join(dir, "keld-agent.prev")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "keld.prev")); err != nil {
		t.Fatal(err)
	}
	r := &fakeRestart{}
	rec := &recorded{}
	err := Confirm(p, "v1", now, 15*time.Minute, r, rec.emit)
	if err == nil {
		t.Fatal("a failed restore must be reported")
	}
	if !strings.Contains(err.Error(), "restore") {
		t.Fatalf("got %v", err)
	}
	if r.count() != 0 {
		t.Fatal("must not restart into an unknown state")
	}
	if !rec.has("update.failed") {
		t.Fatalf("events = %v", rec.events)
	}
	// The marker is still cleared: leaving it pending would roll back again on
	// every subsequent start, forever.
	s, _ := LoadState(p)
	if s.PendingConfirm {
		t.Fatal("a marker that cannot be acted on must not be retried forever")
	}
}

func TestConfirmIgnoresACorruptMarker(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(p, []byte("{{{"), 0o600); err != nil {
		t.Fatal(err)
	}
	r := &fakeRestart{}
	rec := &recorded{}
	if err := Confirm(p, "v1", now, 15*time.Minute, r, rec.emit); err != nil {
		t.Fatal(err)
	}
	if r.count() != 0 {
		t.Fatal("a corrupt marker is not an update in flight")
	}
}

// A marker whose parked copies were never recorded cannot be rolled back; it
// is cleared rather than retried.
func TestConfirmClearsAMarkerWithNoPrevCopies(t *testing.T) {
	p := filepath.Join(t.TempDir(), "state.json")
	if err := SaveState(p, State{From: "v1", To: "v2", PendingConfirm: true, AttemptedAt: now}); err != nil {
		t.Fatal(err)
	}
	r := &fakeRestart{}
	rec := &recorded{}
	_ = Confirm(p, "v1", now, 15*time.Minute, r, rec.emit)
	s, _ := LoadState(p)
	if s.PendingConfirm {
		t.Fatal("marker should not survive")
	}
}
