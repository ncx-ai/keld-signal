package hardware

import (
	"runtime"
	"testing"
	"time"
)

// AC-12: Collect never fails and always fills the fields the host can answer.
func TestCollectBestEffort(t *testing.T) {
	got := Collect()
	if got.LogicalCores < 1 {
		t.Fatalf("cores = %d", got.LogicalCores)
	}
	if runtime.GOOS == "darwin" && (got.CPUModel == "" || got.MemTotalGB < 1) {
		t.Fatalf("darwin should resolve cpu+mem, got %+v", got)
	}
}

// ⚠️ THE STARTUP-WEDGE CASE: Collect runs synchronously in daemon.Run, before
// the sidecar even spawns, so a hung shell-out (stuck filesystem, a
// corrupted sysctl/sw_vers binary) must not be able to block the whole
// daemon's startup. commandOutput is the one seam every exec in this package
// goes through; this drives it with a command that sleeps well past
// execTimeout and asserts it returns the zero value — never an error, never
// blocking anywhere near the sleep's duration.
func TestCommandOutputDoesNotBlockOnAHangingCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no POSIX `sleep` on windows; this package's exec paths are darwin/linux only")
	}

	started := time.Now()
	got := commandOutput("sleep", "5") // 5s: several multiples of execTimeout (2s)
	elapsed := time.Since(started)

	if got != "" {
		t.Fatalf("commandOutput = %q, want \"\" for a command that never produced output", got)
	}
	// Generous slack over execTimeout for process teardown, never anywhere
	// near the full 5s sleep — that is the difference between "bounded" and
	// "eventually returns".
	if elapsed > execTimeout+time.Second {
		t.Fatalf("commandOutput took %s, want at most ~%s (execTimeout) — it blocked on the hanging command", elapsed, execTimeout)
	}
}

// The same shape at the Collect() level for darwin, where sysctlOutput and
// the sw_vers call are actually reached: a hung command must degrade the
// specific field to empty, not fail Collect or the whole snapshot.
func TestSysctlOutputEmptyOnUnknownOrHangingCommand(t *testing.T) {
	if got := sysctlOutput("this.key.does.not.exist"); got != "" {
		t.Fatalf("sysctlOutput(unknown key) = %q, want \"\"", got)
	}
}

// ⚠️ THE FIELD IS ONLY USEFUL IF ITS VALUES ARE A CLOSED SET. It exists to be
// COUNTED across a fleet — "how many machines refuse to run unsigned binaries" —
// and a stray spelling silently lands in its own bucket, understating whichever
// bucket it should have joined. This is the same discipline the dynamics
// vocabularies are pinned with, for the same reason.
func TestSmartAppControlIsFromTheClosedSet(t *testing.T) {
	allowed := map[string]bool{
		"":           true, // not Windows
		"off":        true,
		"on":         true,
		"evaluation": true,
		"absent":     true,
		"unknown":    true,
		"unexpected": true,
	}
	got := Collect().SmartAppControl
	if !allowed[got] {
		t.Fatalf("smart_app_control = %q, which is not one of the documented states", got)
	}
	if runtime.GOOS != "windows" && got != "" {
		t.Fatalf("smart_app_control = %q off Windows; it must stay empty so an absent key "+
			"means \"not Windows\" rather than \"could not tell\"", got)
	}
	if runtime.GOOS == "windows" && got == "" {
		t.Fatal("smart_app_control is empty ON Windows; every Windows machine must report " +
			"a real state, including \"unknown\"")
	}
}

// Collect must never fail, on any platform — it runs synchronously in daemon
// startup, so a registry read that goes wrong must degrade, never propagate.
func TestCollectSurvivesWindowsProbe(t *testing.T) {
	info := Collect()
	if info.LogicalCores <= 0 {
		t.Fatalf("logical_cores = %d; Collect returned a broken snapshot", info.LogicalCores)
	}
}
