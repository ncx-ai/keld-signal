package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/agent/update"
)

func boolp(b bool) *bool    { return &b }
func strp(s string) *string { return &s }

func TestUpdateTargetFromANilRemoteIsNotAnUpdate(t *testing.T) {
	if got := updateTargetFrom(nil); got.Enabled {
		t.Fatalf("got %+v", got)
	}
}

func TestUpdateTargetFromAnAbsentBlockIsNotAnUpdate(t *testing.T) {
	if got := updateTargetFrom(&settings.Remote{}); got.Enabled {
		t.Fatalf("got %+v", got)
	}
}

func TestUpdateTargetFromReadsTheBlock(t *testing.T) {
	r := &settings.Remote{Release: &settings.Release{
		Enabled: boolp(true), Version: strp("v0.4.2"), BaseURL: strp("https://mirror"),
	}}
	got := updateTargetFrom(r)
	if got.Version != "v0.4.2" || got.BaseURL != "https://mirror" || !got.Enabled {
		t.Fatalf("got %+v", got)
	}
}

// The confirm pass must be safe to call on a machine that has never updated —
// which is every machine, the first time.
func TestConfirmPendingUpdateIsANoOpOnAFreshMachine(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	var seen []string
	confirmPendingUpdate(func(code, _ string, _ map[string]any) { seen = append(seen, code) })
	if len(seen) != 0 {
		t.Fatalf("events on a fresh machine: %v", seen)
	}
}

// Events raised before the emitter exists are buffered, not dropped: the
// confirm pass runs before awaitConfig, which can block forever.
func TestBufferedEventsHoldUntilReplay(t *testing.T) {
	b := &bufferedEvents{}
	b.emit("update.rolled_back", "error", map[string]any{"version": "v2"})
	if len(b.ev) != 1 {
		t.Fatalf("got %d", len(b.ev))
	}
	b.replay(nil) // a nil emitter must not panic
	if len(b.ev) != 1 {
		t.Fatal("a nil emitter must not consume the buffer")
	}
}

func TestUpdateIntervalsReadTheEnvironment(t *testing.T) {
	t.Setenv("KELD_UPDATE_MIN_INTERVAL", "5m")
	if got := updateMinInterval(); got != 5*time.Minute {
		t.Fatalf("got %v", got)
	}
	t.Setenv("KELD_UPDATE_MIN_INTERVAL", "nonsense")
	if got := updateMinInterval(); got != time.Hour {
		t.Fatalf("an unparseable value must fall back to the default, got %v", got)
	}
	t.Setenv("KELD_UPDATE_CONFIRM_DEADLINE", "2m")
	if got := updateConfirmDeadline(); got != 2*time.Minute {
		t.Fatalf("got %v", got)
	}
}

// resolveDest must locate the binaries from the RUNNING process, so a test
// binary reports its own directory rather than a guess.
func TestResolveDestUsesTheRunningExecutable(t *testing.T) {
	d, ok := resolveDest()
	if !ok {
		t.Skip("no writable destination for the test binary")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if d.BinDir != filepath.Dir(exe) && !d.Migrated {
		t.Fatalf("BinDir = %q, want %q", d.BinDir, filepath.Dir(exe))
	}
}

// The quiesce wait returns immediately on an empty queue and does not hang on
// a permanently busy one.
func TestQuiesceReturnsOnAnEmptyQueueAndDoesNotHangOnABusyOne(t *testing.T) {
	done := make(chan struct{})
	go func() { quiesceFn(func() int { return 0 })(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("empty queue should return at once")
	}

	ctx, cancel := context.WithCancel(context.Background())
	busy := make(chan struct{})
	go func() { quiesceFn(func() int { return 5 })(ctx); close(busy) }()
	cancel()
	select {
	case <-busy:
	case <-time.After(2 * time.Second):
		t.Fatal("a cancelled context must end the wait")
	}
}

// KELD_HOME isolation: no test in this package may write the developer's real
// ~/.keld — the lesson teleproxy's TestMain already encodes.
func TestUpdateStatePathHonoursKeldHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	u := &update.Updater{}
	_ = u
	if got := updateStatePathForTest(); filepath.Dir(filepath.Dir(got)) != home {
		t.Fatalf("state path %q escaped KELD_HOME %q", got, home)
	}
}
