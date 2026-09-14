package agentcli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/singleton"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// THE STORY: `keld-agent run` against a KELD_HOME whose daemon is already
// running says so and exits — cleanly.
//
// ⚠️ **THE EXIT CODE IS THE ASSERTION, NOT THE MESSAGE.** executeCmd turns any
// error out of the run command into exit 1, and the LaunchAgent's KeepAlive is
// the SuccessfulExit=false dictionary: a clean exit is final, a non-zero one is
// respawned. So a duplicate that exited 1 would be restarted by launchd,
// refuse, exit 1, and be restarted again — forever. That is the
// unconditional-KeepAlive crashloop this repo already paid for once (69 spawns
// in 12 minutes), rebuilt out of the guard meant to prevent duplicates. If this
// test ever reads `want 0` and gets 1, the single-instance work has become a
// worse bug than the one it fixed.
func TestDuplicateRunExitsZeroSoLaunchdLeavesItExited(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	// The daemon that is already running.
	held, err := singleton.Acquire(paths.AgentLockPath())
	if err != nil {
		t.Fatalf("could not stand in for the running daemon: %v", err)
	}
	defer func() { _ = held.Release() }()

	root := NewRootCmd()
	root.SetArgs([]string{"run"})
	var stderr bytes.Buffer
	// The message is written to the command's own error stream (os.Stderr in
	// production), not to executeCmd's — executeCmd only prints returned
	// ERRORS, and this exit is deliberately not one.
	root.SetErr(&stderr)

	// Run blocks forever when it actually starts, so a hang here is a real
	// failure rather than slowness: it means the guard did not fire and this
	// test process became a second daemon.
	code := make(chan int, 1)
	go func() { code <- executeCmd(root, &stderr) }()

	select {
	case got := <-code:
		if got != 0 {
			t.Fatalf("exit code = %d, want 0 — a non-zero exit makes launchd "+
				"respawn the duplicate forever; see this test's comment", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("`run` did not exit; it is running as a second daemon")
	}

	// It must also SAY why. A silent clean exit is indistinguishable from the
	// daemon having started, which is the reading a person in a terminal will
	// take when nothing appears.
	if !strings.Contains(stderr.String(), "already running") {
		t.Fatalf("stderr = %q, want it to say another daemon is already running", stderr.String())
	}
}
