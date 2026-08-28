//go:build darwin || linux

package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// alive reports whether pid still exists. Used only for GRANDCHILDREN, never
// for the supervisor's own child: a grandchild is not this test's child, so
// once it dies it is reaped by its parent (or init) and signal 0 gives a clean
// ESRCH — no zombie window to confuse the answer.
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// waitGone polls until every pid is gone, or d elapses. Returns the first pid
// still alive, or 0.
func waitGone(pids []int, d time.Duration) int {
	deadline := time.Now().Add(d)
	for {
		stuck := 0
		for _, p := range pids {
			if alive(p) {
				stuck = p
				break
			}
		}
		if stuck == 0 {
			return 0
		}
		if time.Now().After(deadline) {
			return stuck
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// readPids reads the newline-separated pids a test script recorded.
func readPids(t *testing.T, path string) []int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading recorded pids from %s: %v", path, err)
	}
	var out []int
	for _, line := range strings.Fields(string(b)) {
		n, err := strconv.Atoi(line)
		if err != nil {
			t.Fatalf("pid file held %q: %v", line, err)
		}
		out = append(out, n)
	}
	return out
}

// TestStopChildReapsTheWholeProcessTree is the unit-level statement of the
// defect: a supervised child that has spawned children of its own must leave
// NO survivors when the supervisor stops it.
//
// The scripts stand in for the sidecar and its multiprocessing children. Real
// weights are not needed to prove the mechanism — a grandchild is a grandchild
// — but a real end-to-end run against the actual sidecar is what proves the
// mechanism reaches THIS sidecar, and is recorded in the commit message.
func TestStopChildReapsTheWholeProcessTree(t *testing.T) {
	cases := []struct {
		name string
		// script runs as the supervised "sidecar". $P is a file it must append
		// its children's pids to; $M a marker it writes iff it handled SIGTERM.
		script string
		// stopGrace for this case.
		stopGrace time.Duration
		// wantGraceful: the child is expected to have observed SIGTERM, i.e.
		// the graceful half ran BEFORE the forceful one.
		wantGraceful bool
		// wantAtLeast: the stop must not complete faster than this (the grace
		// period actually being waited out for a child that ignores SIGTERM).
		wantAtLeast time.Duration
		// wantAtMost bounds the stop — a supervisor that hangs is its own
		// failure, so every case has a ceiling.
		wantAtMost time.Duration
	}{
		{
			// The happy path the fix exists to make reachable: the child gets a
			// catchable signal, runs its own teardown (here: writing $M and
			// killing its children, standing in for lifespan's wm.shutdown()
			// and _TEXT_SOURCE.shutdown()), and exits well inside the grace.
			name:         "graceful shutdown runs and is not pre-empted",
			script:       `trap 'echo term > "$M"; kill $C1 $C2 2>/dev/null; exit 0' TERM; sleep 300 & C1=$!; echo $C1 >> "$P"; sleep 300 & C2=$!; echo $C2 >> "$P"; wait`,
			stopGrace:    3 * time.Second,
			wantGraceful: true,
			wantAtMost:   2 * time.Second,
		},
		{
			// A child that IGNORES SIGTERM is the wedge case. The grace must be
			// waited out (not skipped) and then the group killed (not waited on
			// forever) — both halves of "bounded" asserted at once.
			name:         "wedged child ignores SIGTERM and is still killed, within the bound",
			script:       `trap '' TERM; sleep 300 & echo $! >> "$P"; sleep 300 & echo $! >> "$P"; wait`,
			stopGrace:    400 * time.Millisecond,
			wantGraceful: false,
			wantAtLeast:  400 * time.Millisecond,
			wantAtMost:   3 * time.Second,
		},
		{
			// The leak's exact shape: the child dies but its children do not
			// know it. Here the parent exits on SIGTERM WITHOUT killing them,
			// which is what SIGKILL used to force on the real sidecar by
			// denying it the chance to. The unconditional group sweep after the
			// graceful half is the only thing that reaps these.
			name:         "orphans left behind by a clean parent exit are still swept",
			script:       `trap 'exit 0' TERM; sleep 300 & echo $! >> "$P"; sleep 300 & echo $! >> "$P"; wait`,
			stopGrace:    3 * time.Second,
			wantGraceful: false, // no $M written — it exits without a marker
			wantAtMost:   2 * time.Second,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			pidFile := filepath.Join(dir, "pids")
			marker := filepath.Join(dir, "term")
			if err := os.WriteFile(pidFile, nil, 0o600); err != nil {
				t.Fatal(err)
			}

			spawn := func(int) (*exec.Cmd, error) {
				c := exec.Command("sh", "-c", tc.script)
				c.Env = append(os.Environ(), "P="+pidFile, "M="+marker)
				return c, nil
			}
			// health false forever: the supervisor sits in the poll loop and we
			// cancel before readyTimeout, exercising the ctx.Done stop path.
			s := NewSupervisor(spawn, 0, func() bool { return false }, 30*time.Second)
			s.stopGrace = tc.stopGrace

			ctx, cancel := context.WithCancel(context.Background())
			go s.Start(ctx)

			// Wait for the script to have recorded both grandchildren.
			var kids []int
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if b, err := os.ReadFile(pidFile); err == nil && len(strings.Fields(string(b))) == 2 {
					kids = readPids(t, pidFile)
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if len(kids) != 2 {
				cancel()
				t.Fatalf("script did not record 2 child pids within 5s (got %v)", kids)
			}
			for _, k := range kids {
				if !alive(k) {
					cancel()
					t.Fatalf("precondition: recorded child %d is not running", k)
				}
			}

			start := time.Now()
			cancel()
			if !s.AwaitStopped(tc.wantAtMost) {
				t.Fatalf("supervisor did not finish stopping within %s", tc.wantAtMost)
			}
			elapsed := time.Since(start)

			// THE ASSERTION THIS FILE EXISTS FOR: no survivors.
			if stuck := waitGone(kids, 2*time.Second); stuck != 0 {
				_ = syscall.Kill(stuck, syscall.SIGKILL) // never leak from a failing test
				t.Fatalf("child %d survived the supervisor's stop (recorded pids %v)", stuck, kids)
			}

			if tc.wantAtLeast > 0 && elapsed < tc.wantAtLeast {
				t.Fatalf("stop took %s, want at least the %s grace — the graceful half was skipped",
					elapsed, tc.wantAtLeast)
			}
			if elapsed > tc.wantAtMost {
				t.Fatalf("stop took %s, want at most %s", elapsed, tc.wantAtMost)
			}

			_, err := os.Stat(marker)
			gotGraceful := err == nil
			if gotGraceful != tc.wantGraceful {
				t.Fatalf("child observed SIGTERM = %v, want %v (marker %s)", gotGraceful, tc.wantGraceful, marker)
			}
		})
	}
}

// TestGracefulPrecedesForceful pins the ORDER rather than just the outcome. A
// child that exits on SIGTERM and records nothing else still proves ordering:
// if the forceful half ran first it would be SIGKILLed, its trap would never
// run, and the marker would be absent.
func TestGracefulPrecedesForceful(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "term")
	spawn := func(int) (*exec.Cmd, error) {
		c := exec.Command("sh", "-c", `trap 'echo term > "$M"; exit 0' TERM; while :; do sleep 0.05; done`)
		c.Env = append(os.Environ(), "M="+marker)
		return c, nil
	}
	s := NewSupervisor(spawn, 0, func() bool { return false }, 30*time.Second)
	s.stopGrace = 3 * time.Second

	ctx, cancel := context.WithCancel(context.Background())
	go s.Start(ctx)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && s.Pid() == 0; {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Pid() == 0 {
		cancel()
		t.Fatal("child never started")
	}
	cancel()
	if !s.AwaitStopped(3 * time.Second) {
		t.Fatal("supervisor did not stop in time")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("SIGTERM never reached the child — the forceful half ran first: %v", err)
	}
}

// TestSpawnedChildLeadsItsOwnProcessGroup pins the mechanism the kill path
// depends on. ⚠️ If this ever fails, killProcessTree silently falls back to a
// pid-only kill and the leak is back with no test failing anywhere else.
func TestSpawnedChildLeadsItsOwnProcessGroup(t *testing.T) {
	s := NewSupervisor(func(int) (*exec.Cmd, error) { return exec.Command("sleep", "30"), nil },
		0, func() bool { return false }, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	go s.Start(ctx)
	var pid int
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		if pid = s.Pid(); pid != 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if pid == 0 {
		cancel()
		t.Fatal("child never started")
	}
	pgid, group := childGroup(pid)
	if !group || pgid != pid {
		cancel()
		t.Fatalf("child %d is not its own group leader (pgid %d, ok %v)", pid, pgid, group)
	}
	if pgid == syscall.Getpgrp() {
		cancel()
		t.Fatalf("child shares the TEST's process group (%d) — signalling it would be suicide", pgid)
	}
	cancel()
	s.AwaitStopped(3 * time.Second)
}

// TestChildGroupRefusesAGroupWeDoNotOwn is the safety guard's own test: a
// process that is NOT its own group leader must report false, so the caller
// signals a single pid instead of a group that includes the daemon.
func TestChildGroupRefusesAGroupWeDoNotOwn(t *testing.T) {
	// No setProcessGroup: this child inherits the test binary's group.
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() }()

	if _, group := childGroup(cmd.Process.Pid); group {
		t.Fatalf("childGroup approved pid %d, which shares our group %d — kill(-pgid) there would signal the daemon itself",
			cmd.Process.Pid, syscall.Getpgrp())
	}
}

// TestStopWithNoChildIsANoOp preserves the pre-fix behaviour when there is
// nothing to reap: a spawn that fails must not leave the supervisor waiting on
// a grace period or blocking on a waitCh that has no producer.
func TestStopWithNoChildIsANoOp(t *testing.T) {
	s := NewSupervisor(func(int) (*exec.Cmd, error) { return nil, os.ErrNotExist },
		0, func() bool { return false }, 30*time.Second)
	s.stopGrace = 10 * time.Second // would dominate if it were ever waited
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	start := time.Now()
	go s.Start(ctx)
	if !s.AwaitStopped(2 * time.Second) {
		t.Fatal("a supervisor with no child must return immediately")
	}
	if d := time.Since(start); d > time.Second {
		t.Fatalf("returned after %s — a grace period was waited with no child to wait for", d)
	}
	if !s.FellBack() {
		t.Fatal("a failed spawn must still fall back")
	}
}

// TestRestartPathSurvivesTheProcessGroup: Setpgid changes signal delivery, so
// the restart/backoff loop is re-pinned against it. Every generation must get
// its OWN group — a restart that reused or lost the group would make the next
// stop reap the wrong tree, or none.
func TestRestartPathSurvivesTheProcessGroup(t *testing.T) {
	var spawns atomic.Int64
	spawn := func(int) (*exec.Cmd, error) {
		spawns.Add(1)
		// Exits on its own, which is what drives the restart loop.
		return exec.Command("sh", "-c", "sleep 0.05"), nil
	}
	s := NewSupervisor(spawn, 0, func() bool { return false }, 5*time.Second)
	s.stopGrace = 200 * time.Millisecond

	// Sample each generation's pgid as it appears. A mutex-guarded map plus an
	// explicit join, rather than a channel closed from the test goroutine: the
	// sampler outlives the supervisor by up to one tick, and closing under it
	// is a race the detector rightly rejects.
	var mu sync.Mutex
	pgidOf := map[int]int{} // child pid -> its pgid
	stopSampler := make(chan struct{})
	samplerDone := make(chan struct{})
	go func() {
		defer close(samplerDone)
		for {
			select {
			case <-stopSampler:
				return
			default:
			}
			if pid := s.Pid(); pid != 0 {
				mu.Lock()
				_, known := pgidOf[pid]
				mu.Unlock()
				if !known {
					if pgid, ok := childGroup(pid); ok {
						mu.Lock()
						pgidOf[pid] = pgid
						mu.Unlock()
					}
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go s.Start(ctx)
	if !s.AwaitStopped(8 * time.Second) {
		close(stopSampler)
		<-samplerDone
		t.Fatal("supervisor did not exhaust its restart budget in time")
	}
	close(stopSampler)
	<-samplerDone

	if got := spawns.Load(); got != maxRestarts+1 {
		t.Fatalf("spawned %d times, want %d (the restart path regressed)", got, maxRestarts+1)
	}
	if !s.FellBack() {
		t.Fatal("expected FellBack after the restart cap")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(pgidOf) == 0 {
		t.Fatal("sampled no generation's process group — the test observed nothing")
	}
	seen := map[int]bool{}
	for pid, pgid := range pgidOf {
		if pgid != pid {
			t.Fatalf("generation %d is not its own group leader (pgid %d)", pid, pgid)
		}
		if seen[pgid] {
			t.Fatalf("two generations shared process group %d", pgid)
		}
		seen[pgid] = true
	}
}

// TestRestartAfterAKilledChildComesUpClean: stop a supervised child, then bring
// a fresh supervisor up on the same spawn and prove it reaches ready. This is
// the "killed-then-restarted sidecar must come up clean" requirement, with the
// process group in place across both.
func TestRestartAfterAKilledChildComesUpClean(t *testing.T) {
	spawn := func(int) (*exec.Cmd, error) { return exec.Command("sleep", "30"), nil }

	var healthy atomic.Bool
	healthy.Store(true)

	first := NewSupervisor(spawn, 0, func() bool { return healthy.Load() }, 5*time.Second)
	first.stopGrace = time.Second
	ctx1, cancel1 := context.WithCancel(context.Background())
	go first.Start(ctx1)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && !first.Ready(); {
		time.Sleep(10 * time.Millisecond)
	}
	if !first.Ready() {
		cancel1()
		t.Fatal("first supervisor never became ready")
	}
	firstPid := first.Pid()
	cancel1()
	if !first.AwaitStopped(3 * time.Second) {
		t.Fatal("first supervisor did not stop")
	}

	second := NewSupervisor(spawn, 0, func() bool { return healthy.Load() }, 5*time.Second)
	second.stopGrace = time.Second
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer func() { cancel2(); second.AwaitStopped(3 * time.Second) }()
	go second.Start(ctx2)
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline) && !second.Ready(); {
		time.Sleep(10 * time.Millisecond)
	}
	if !second.Ready() {
		t.Fatal("a supervisor started after a killed child did not come up clean")
	}
	if second.Pid() == firstPid {
		t.Fatalf("second supervisor reports the dead child's pid %d", firstPid)
	}
}

// TestStopGraceFromEnv covers the one knob, including the two ways of asking
// for a zero grace — which must NOT be honoured: a zero grace skips the
// graceful path this whole change exists to make reachable.
func TestStopGraceFromEnv(t *testing.T) {
	cases := []struct {
		name string
		set  string
		want time.Duration
	}{
		{"unset uses the default", "", DefaultStopGrace},
		{"a valid duration is honoured", "2s", 2 * time.Second},
		{"unparseable falls back", "soon", DefaultStopGrace},
		{"zero falls back", "0s", DefaultStopGrace},
		{"negative falls back", "-1s", DefaultStopGrace},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.set == "" {
				os.Unsetenv("KELD_SIDECAR_STOP_GRACE")
			} else {
				t.Setenv("KELD_SIDECAR_STOP_GRACE", tc.set)
			}
			if got := stopGraceFromEnv(); got != tc.want {
				t.Fatalf("stopGraceFromEnv() = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestAwaitSidecarStopIsNilWithoutASupervisor: a run with no supervised sidecar
// must hand Run nothing to wait for.
func TestAwaitSidecarStopIsNilWithoutASupervisor(t *testing.T) {
	if awaitSidecarStop(nil) != nil {
		t.Fatal("awaitSidecarStop(nil) must be nil so Run waits for nothing")
	}
	s := NewSupervisor(func(int) (*exec.Cmd, error) { return exec.Command("true"), nil },
		0, func() bool { return false }, 50*time.Millisecond)
	fn := awaitSidecarStop(s)
	if fn == nil {
		t.Fatal("a real supervisor must produce a wait func")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Start(ctx)
	done := make(chan struct{})
	go func() { fn(); close(done) }()
	select {
	case <-done:
	case <-time.After(s.StopGrace() + 3*time.Second):
		t.Fatal("awaitSidecarStop did not return within its own bound")
	}
}
