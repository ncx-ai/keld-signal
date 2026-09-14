package daemon

import (
	"context"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// These four tests pin the two rules that replaced permanent surrender on
// 2026-09-09, after a night of macOS dark wakes stranded a machine with no
// analysis service until a human restarted the daemon:
//
//  1. A readiness deadline is measured in time the machine was AWAKE. A wall
//     clock jump between two health polls is a sleep, and a sleep re-arms the
//     deadline instead of killing a child that never got to run.
//  2. The supervisor never gives up for the life of the daemon. After the fast
//     retries it RESTS — a growing, bounded pause — and tries again on its own,
//     or immediately when asked (the page's Restart button, the health owner).
//
// Measured before this: a 90s deadline was set at 03:19, the machine slept
// with ~2s dark wakes every 15 minutes, and at 05:20, 07:23 and 09:42 the
// supervisor killed a sidecar that had been given seconds of real time as a
// "failed start". The third one exhausted the cap and `RequestRestart` refused
// forever after. `pmset -g log` and the daemon log agree to the second.

// TestASleepDuringTheStartDoesNotFailIt: the wall clock jumps an hour while
// the sidecar is still binding its port. A deadline read off that clock would
// fire on the next poll and kill the child; the supervisor must instead notice
// the gap and give the child its full readiness window again.
func TestASleepDuringTheStartDoesNotFailIt(t *testing.T) {
	var spawns atomic.Int32
	var healthy atomic.Bool
	var skew atomic.Int64 // how far the fake clock is ahead of the real one
	s := NewSupervisor(func(int) (*exec.Cmd, error) { spawns.Add(1); return sleepCmd() }, 0,
		func() bool { return healthy.Load() }, time.Second)
	s.now = func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Start(ctx)
	waitFor(t, 2*time.Second, func() bool { return s.Pid() != 0 })
	pid := s.Pid()
	time.Sleep(150 * time.Millisecond) // a few polls with the deadline honestly ahead

	skew.Store(int64(time.Hour))       // the machine slept for an hour mid-start
	time.Sleep(400 * time.Millisecond) // several polls; a wall-clock check would have fired on the first

	if got := spawns.Load(); got != 1 {
		t.Fatalf("spawns = %d; the sidecar was killed for a start that merely spanned a sleep", got)
	}
	if s.Pid() != pid {
		t.Fatal("the child was replaced after the sleep; the readiness deadline was not re-armed")
	}
	healthy.Store(true)
	waitFor(t, 2*time.Second, func() bool { return s.Ready() })
	if s.FellBack() {
		t.Fatal("the supervisor is resting after one start that spanned a sleep")
	}
}

// TestTheSupervisorRestsAndTriesAgainInsteadOfSurrendering: a child that
// exits immediately exhausts the fast retries, and the supervisor then reports
// it has fallen back — but keeps spawning after the rest, and Start does not
// return.
func TestTheSupervisorRestsAndTriesAgainInsteadOfSurrendering(t *testing.T) {
	var spawns atomic.Int32
	s := NewSupervisor(func(int) (*exec.Cmd, error) { spawns.Add(1); return exec.Command("true"), nil }, 0,
		func() bool { return false }, 50*time.Millisecond)
	s.restBase, s.restMax = 100*time.Millisecond, 100*time.Millisecond
	emitter := enabledEmitter()
	s.SetEmitter(emitter)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Start(ctx)

	waitFor(t, 6*time.Second, func() bool { return s.FellBack() })
	atCap := spawns.Load()
	if atCap < 1+maxRestarts {
		t.Fatalf("spawns at the cap = %d, want at least %d (the original plus the fast retries)", atCap, 1+maxRestarts)
	}
	waitFor(t, 6*time.Second, func() bool { return spawns.Load() > atCap })
	if s.AwaitStopped(10 * time.Millisecond) {
		t.Fatal("Start returned: the supervisor surrendered instead of resting")
	}

	ev := findEvent(emitter.Drain(), "sidecar.unavailable")
	if ev == nil {
		t.Fatal("entering the rest must still emit sidecar.unavailable — a fleet view has to see the outage")
	}
	if _, ok := ev.Fields["retry_in_s"]; !ok {
		t.Fatalf("sidecar.unavailable must say when the next attempt is, got fields %+v", ev.Fields)
	}
}

// TestARestartRequestDuringTheRestTriesNow: the page's Restart button and the
// health owner both call RequestRestart. While the supervisor is resting that
// must not be refused — it is exactly the moment a person is asking for the
// attempt to happen now rather than in half an hour.
func TestARestartRequestDuringTheRestTriesNow(t *testing.T) {
	var spawns atomic.Int32
	s := NewSupervisor(func(int) (*exec.Cmd, error) { spawns.Add(1); return exec.Command("true"), nil }, 0,
		func() bool { return false }, 50*time.Millisecond)
	s.restBase, s.restMax = time.Hour, time.Hour // nothing happens on its own inside this test

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Start(ctx)

	waitFor(t, 6*time.Second, func() bool { return s.FellBack() })
	before := spawns.Load()
	time.Sleep(200 * time.Millisecond)
	if spawns.Load() != before {
		t.Fatal("the supervisor spawned during a one-hour rest; the rest is not being honoured")
	}
	if err := s.RequestRestart(); err != nil {
		t.Fatalf("RequestRestart during the rest = %v; a resting supervisor must accept the request", err)
	}
	waitFor(t, 2*time.Second, func() bool { return spawns.Load() > before })
}

// TestTheCrashBudgetResetsOnceAChildWasHealthy: the cap bounds CONSECUTIVE
// failed starts. A sidecar that came up, ran, and was killed four times over
// the daemon's life is four recoveries, not a crash loop — under the old
// accounting the fourth kill was permanent surrender.
func TestTheCrashBudgetResetsOnceAChildWasHealthy(t *testing.T) {
	var spawns atomic.Int32
	s := NewSupervisor(func(int) (*exec.Cmd, error) { spawns.Add(1); return sleepCmd() }, 0,
		func() bool { return true }, 2*time.Second)
	s.restBase, s.restMax = time.Hour, time.Hour // a rest here would be visible as a stall

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Start(ctx)

	for i := 0; i < maxRestarts+1; i++ {
		waitFor(t, 5*time.Second, func() bool { return s.Ready() && s.Pid() != 0 })
		pid := s.Pid()
		if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
			t.Fatalf("kill %d: %v", pid, err)
		}
		waitFor(t, 10*time.Second, func() bool { p := s.Pid(); return p != 0 && p != pid })
	}
	if s.FellBack() {
		t.Fatal("four crashes of a child that was healthy in between exhausted the budget; the count must reset on ready")
	}
	if got := spawns.Load(); got < int32(maxRestarts+2) {
		t.Fatalf("spawns = %d, want at least %d", got, maxRestarts+2)
	}
}

// waitForCond is waitFor without the t.Fatal, for callers that have their own
// goroutines to join before failing.
func waitForCond(d time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
