package daemon

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/singleton"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// THE STORY: a second daemon exits immediately saying one is already running,
// and the daemon that was already collecting is untouched.
//
// The interesting assertion is not the returned error but agent.json's ABSENCE.
// Run binds its listener and publishes agent.json very early — before
// awaitConfig, deliberately, so an unconfigured machine can still be paired
// through the page. So a Run that produced no agent.json cannot have reached
// the listener, and therefore cannot have reached reapStaleSidecars either,
// which is much further down. That ordering is the whole point of taking the
// lock first: the reaper kills every process matching the sidecar's basename
// machine-wide, so a duplicate that reaped before it refused would have killed
// the LIVE daemon's sidecar on its way out — the guard causing exactly the
// damage it exists to prevent.
func TestSecondDaemonRefusesAndTouchesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	// Stand in for the daemon that is already running. Holding the real lock
	// path rather than a stub is the point: this test fails if Run ever stops
	// consulting paths.AgentLockPath.
	held, err := singleton.Acquire(paths.AgentLockPath())
	if err != nil {
		t.Fatalf("the first daemon could not take the lock: %v", err)
	}
	defer func() { _ = held.Release() }()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()

	select {
	case err := <-done:
		if !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("Run() = %v, want ErrAlreadyRunning", err)
		}
	case <-time.After(10 * time.Second):
		// Not a timeout to be lengthened. Run blocks forever by design when it
		// starts (awaitConfig), so reaching here means the guard did not fire
		// and this process is now a second live daemon.
		t.Fatal("Run did not refuse; it is running as a second daemon")
	}

	if _, err := os.Stat(filepath.Join(home, "agent.json")); !os.IsNotExist(err) {
		t.Fatalf("the refused daemon published agent.json (stat err = %v); it "+
			"got far enough to bind a listener, so it also got far enough to "+
			"reap the running daemon's sidecar", err)
	}
}

// NEGATIVE: a lock left behind by a daemon that is GONE must not stop the next
// one. This is the failure mode a pid file would have — the file outlives its
// writer, so a SIGKILLed daemon locks Signal out until someone deletes it by
// hand. Here the file is present and stale, and Run must get past it.
//
// Run is then cancelled rather than left going: this only needs to prove the
// guard let it through, and letting a real daemon start inside a unit test
// would spawn a sidecar.
func TestAStaleLockFileDoesNotBlockStartup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	t.Setenv("KELD_CONFIG_POLL", "20ms")

	// A lock file with a plausible pid in it and nobody holding it — exactly
	// what a killed daemon leaves on disk.
	if err := os.WriteFile(paths.AgentLockPath(), []byte("999999"), 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()

	// Getting past the guard is observable as agent.json appearing: that is the
	// first thing Run does after taking the lock.
	discovery := filepath.Join(home, "agent.json")
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(discovery); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("Run exited instead of starting: %v — a stale lock file "+
				"locked the daemon out, which is the pid-file failure this "+
				"design exists to avoid", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("Run never published agent.json; it did not get past the stale lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
