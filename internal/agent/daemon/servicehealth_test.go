package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// probeStub is a health probe a test drives: answers[i] is what the i-th probe
// returns, and the last value repeats forever.
type probeStub struct {
	mu      sync.Mutex
	answers []bool
	calls   int
	block   chan struct{} // when non-nil, the probe waits on it before answering
}

func (p *probeStub) probe(ctx context.Context) bool {
	p.mu.Lock()
	blocked := p.block
	i := p.calls
	p.calls++
	answers := p.answers
	p.mu.Unlock()
	if blocked != nil {
		select {
		case <-blocked:
		case <-ctx.Done():
			return false
		}
	}
	if len(answers) == 0 {
		return false
	}
	if i >= len(answers) {
		return answers[len(answers)-1]
	}
	return answers[i]
}

// recorder collects emitted client events so a test can assert on the pattern
// the fleet would see, not merely on the local state machine.
type recorder struct {
	mu     sync.Mutex
	events []string
	fields []map[string]any
}

func (r *recorder) emit(code string, _ clientevents.Severity, f map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, code)
	r.fields = append(r.fields, f)
}

func (r *recorder) count(code string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, c := range r.events {
		if c == code {
			n++
		}
	}
	return n
}

// newTestHealth builds an owner whose marker lives in a temp KELD_HOME. It does
// NOT start the loop — every ladder test drives check() by hand, so the
// assertions are about the ladder rather than about a timer.
func newTestHealth(t *testing.T, probe func(context.Context) bool, restartSidecar, restartDaemon func() error, rec *recorder) *serviceHealth {
	t.Helper()
	t.Setenv("KELD_HOME", t.TempDir())
	var emit func(string, clientevents.Severity, map[string]any)
	if rec != nil {
		emit = rec.emit
	}
	return newServiceHealth(probe, "no analysis service installed.", restartSidecar, restartDaemon, emit, emit)
}

// TestOneFailedProbeRestartsNothing pins the bottom of the ladder.
//
// ⚠️ Remove this and a single unanswered health check — a loaded machine, a
// paused laptop, one slow loopback call — becomes a sidecar restart. That is a
// detector causing the outage it exists to find, and it is the failure mode
// that makes people turn self-healing off.
func TestOneFailedProbeRestartsNothing(t *testing.T) {
	restarts := 0
	rec := &recorder{}
	h := newTestHealth(t, (&probeStub{answers: []bool{false}}).probe,
		func() error { restarts++; return nil },
		func() error { t.Fatal("one failure must never restart the daemon"); return nil },
		rec)

	h.check(context.Background())

	if restarts != 0 {
		t.Fatalf("sidecar restarts = %d after ONE failure, want 0", restarts)
	}
	got := h.Snapshot()
	if got.State != string(serviceDegraded) {
		t.Fatalf("state = %q after one failure, want %q", got.State, serviceDegraded)
	}
	if got.Failures != 1 {
		t.Fatalf("failures = %d, want 1", got.Failures)
	}
	if got.Reason == "" {
		t.Fatal("degraded must carry a reason; a state with no sentence is unreadable on the page")
	}
	if rec.count("service.restarted") != 0 {
		t.Fatal("no restart happened, so no service.restarted may be emitted")
	}
}

// TestTwoFailedProbesStillRestartNothing pins the other side of "1-2 is noise".
func TestTwoFailedProbesStillRestartNothing(t *testing.T) {
	restarts := 0
	h := newTestHealth(t, (&probeStub{answers: []bool{false}}).probe,
		func() error { restarts++; return nil }, nil, nil)

	h.check(context.Background())
	h.check(context.Background())

	if restarts != 0 {
		t.Fatalf("sidecar restarts = %d after TWO failures, want 0 — the rung is three", restarts)
	}
	if got := h.Snapshot(); got.State != string(serviceDegraded) || got.Failures != 2 {
		t.Fatalf("snapshot = %#v, want degraded with 2 failures", got)
	}
}

// TestThreeConsecutiveFailuresRestartTheSidecarExactlyOnce is the whole reason
// the rungs fire on equality rather than on >=.
//
// ⚠️ Remove this and the natural rewrite (`if n >= 3 { restart }`) issues a
// restart on EVERY probe from the third onwards — one every 15 seconds against
// a service that needs seconds to come up, which is a restart loop that can
// never resolve. The count of restarts is the assertion, not the fact that one
// happened.
func TestThreeConsecutiveFailuresRestartTheSidecarExactlyOnce(t *testing.T) {
	var restarts int32
	rec := &recorder{}
	h := newTestHealth(t, (&probeStub{answers: []bool{false}}).probe,
		func() error { atomic.AddInt32(&restarts, 1); return nil },
		func() error { return nil }, rec)

	for i := 0; i < 5; i++ { // five probes: rungs at 3, nothing at 4 or 5
		h.check(context.Background())
	}

	if got := atomic.LoadInt32(&restarts); got != 1 {
		t.Fatalf("sidecar restarts = %d across five consecutive failures, want exactly 1", got)
	}
	if n := rec.count("service.restarted"); n != 1 {
		t.Fatalf("service.restarted emitted %d times, want 1", n)
	}
	if got := h.Snapshot(); got.State != string(serviceRestarting) {
		t.Fatalf("state = %q after the restart rung, want %q", got.State, serviceRestarting)
	}
}

// TestASuccessResetsTheConsecutiveFailureCount.
//
// ⚠️ Remove this and the counter stops meaning "consecutive" and starts meaning
// "ever". On a healthy machine that misses one probe an hour, it crosses the
// sidecar rung after three hours and the daemon rung after six — a self-restart
// caused entirely by arithmetic, on a machine with nothing wrong with it.
func TestASuccessResetsTheConsecutiveFailureCount(t *testing.T) {
	var restarts int32
	rec := &recorder{}
	// fail, fail, SUCCEED, fail, fail — never three in a row.
	p := &probeStub{answers: []bool{false, false, true, false, false}}
	h := newTestHealth(t, p.probe,
		func() error { atomic.AddInt32(&restarts, 1); return nil },
		func() error { t.Fatal("the daemon must not restart when no streak reaches the rung"); return nil }, rec)

	for i := 0; i < 5; i++ {
		h.check(context.Background())
	}

	if got := atomic.LoadInt32(&restarts); got != 0 {
		t.Fatalf("sidecar restarts = %d, want 0 — a success in the middle means no streak ever reached three", got)
	}
	if got := h.Snapshot(); got.Failures != 2 {
		t.Fatalf("failures = %d after fail,fail,ok,fail,fail — want 2, the length of the CURRENT streak", got.Failures)
	}
	if n := rec.count("service.recovered"); n != 1 {
		t.Fatalf("service.recovered emitted %d times, want 1 — a recovery must be distinguishable from a machine that went quiet", n)
	}
}

// TestNoSidecarInstalledReportsNotApplicableAndNeverRestarts is the
// load-bearing one.
//
// ⚠️ Remove this and the single most dangerous mistake available in this file
// becomes invisible. daemon.go deliberately runs WITHOUT window analysis when
// no sidecar binary is installed (noAnalysisService) — that is a dropped facet,
// reported dropped. An owner that treated it as unhealthy would restart the
// sidecar (there isn't one), then the daemon, on EVERY machine in the fleet
// that has not fetched the sidecar tarball, forever.
func TestNoSidecarInstalledReportsNotApplicableAndNeverRestarts(t *testing.T) {
	h := newTestHealth(t, nil, // nil probe IS the no-service condition
		func() error { t.Fatal("a machine with no analysis service must never restart the sidecar"); return nil },
		func() error { t.Fatal("a machine with no analysis service must never restart the daemon"); return nil },
		nil)

	got := h.Snapshot()
	if got.State != string(serviceNotApplicable) {
		t.Fatalf("state = %q with no sidecar installed, want %q", got.State, serviceNotApplicable)
	}
	if got.Failures != 0 {
		t.Fatalf("failures = %d, want 0 — nothing was ever probed", got.Failures)
	}

	// run must return immediately rather than start a ticker against nothing.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { h.run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("run must return at once when there is no service to watch, not sit on a ticker")
	}

	// And the strip must not be able to read a verdict out of it.
	if _, known := h.Healthy(); known {
		t.Fatal("a not-applicable owner must report known=false; the strip decides n/a from its own nil probe")
	}
}

// TestDaemonRestartIsAttemptedAtMostOnceAndTheFactSurvivesARestart.
//
// ⚠️ Remove this and the daemon-restart rung becomes an unbounded self-restart
// loop, which is the crash loop one level up at a much larger blast radius —
// this repo already has an incident of 69 launchd spawns in 12 minutes from
// that exact shape. The second half of the assertion is the part that is easy
// to get wrong: after service.Restart() the daemon is a NEW PROCESS, so an
// in-memory "already tried" is true for exactly as long as it takes to be
// useless. The marker file is what makes the bound real.
func TestDaemonRestartIsAttemptedAtMostOnceAndTheFactSurvivesARestart(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	rec := &recorder{}

	var sidecarRestarts, daemonRestarts int32
	build := func() *serviceHealth {
		return newServiceHealth((&probeStub{answers: []bool{false}}).probe,
			"", func() error { atomic.AddInt32(&sidecarRestarts, 1); return nil },
			func() error { atomic.AddInt32(&daemonRestarts, 1); return nil },
			rec.emit, rec.emit)
	}

	first := build()
	for i := 0; i < 9; i++ { // well past both rungs
		first.check(context.Background())
	}
	if got := atomic.LoadInt32(&daemonRestarts); got != 1 {
		t.Fatalf("daemon restarts = %d in one process across nine consecutive failures, want exactly 1", got)
	}
	if got := first.Snapshot(); got.State != string(serviceStuck) {
		t.Fatalf("state = %q after the daemon rung was spent, want %q", got.State, serviceStuck)
	}

	// The marker is on disk, where the next PROCESS can read it.
	markerPath := filepath.Join(home, "state", "service-health.json")
	b, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatalf("the daemon-restart marker must be written before the restart: %v", err)
	}
	var m serviceHealthMarker
	if json.Unmarshal(b, &m) != nil || m.DaemonRestartedAt.IsZero() {
		t.Fatalf("marker = %s, want a daemon_restarted_at instant", b)
	}

	// Now simulate the restart: a brand-new owner, same machine, same home.
	second := build()
	for i := 0; i < 9; i++ {
		second.check(context.Background())
	}
	if got := atomic.LoadInt32(&daemonRestarts); got != 1 {
		t.Fatalf("daemon restarts = %d across TWO daemon lifetimes, want 1 — the marker did not survive the restart", got)
	}
	if got := second.Snapshot(); got.State != string(serviceStuck) {
		t.Fatalf("state = %q in the second process, want %q", got.State, serviceStuck)
	}
	if !strings.Contains(second.Snapshot().Reason, "restarted") {
		t.Fatalf("stuck reason = %q; it must name what was already TRIED, not merely that something broke", second.Snapshot().Reason)
	}
}

// TestARestartWeCannotRecordIsARestartWeDoNotPerform.
//
// ⚠️ Remove this and the marker write becomes best-effort, which quietly
// removes the ONLY bound on the daemon-restart rung: a machine whose state
// directory is unwritable would restart, come back, find no memory of the
// attempt, and restart again. The safe direction is to stay broken and say so.
func TestARestartWeCannotRecordIsARestartWeDoNotPerform(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	// Make the marker path unwritable by putting a DIRECTORY where the file
	// goes: os.WriteFile then fails on every platform, with no chmod games.
	if err := os.MkdirAll(filepath.Join(home, "state", "service-health.json"), 0o700); err != nil {
		t.Fatal(err)
	}

	var daemonRestarts int32
	h := newServiceHealth((&probeStub{answers: []bool{false}}).probe, "",
		func() error { return nil },
		func() error { atomic.AddInt32(&daemonRestarts, 1); return nil },
		nil, nil)

	for i := 0; i < 8; i++ {
		h.check(context.Background())
	}

	if got := atomic.LoadInt32(&daemonRestarts); got != 0 {
		t.Fatalf("daemon restarts = %d with an unrecordable marker, want 0 — an unbounded rung is worse than a broken machine", got)
	}
	if got := h.Snapshot(); got.State != string(serviceStuck) {
		t.Fatalf("state = %q, want %q — refusing to restart must still be REPORTED", got.State, serviceStuck)
	}
}

// TestASurrenderedSupervisorIsReportedRatherThanRetried covers S5: the
// supervisor exhausted its restart cap, so there is nothing left to restart.
//
// ⚠️ Remove this and the state that stranded a machine on 4 September — three
// crashes, cap exceeded, one sidecar.unavailable, then permanent silence — goes
// back to being invisible to the page.
func TestASurrenderedSupervisorIsReportedRatherThanRetried(t *testing.T) {
	rec := &recorder{}
	var attempts int32
	h := newTestHealth(t, (&probeStub{answers: []bool{false}}).probe,
		func() error { atomic.AddInt32(&attempts, 1); return ErrSupervisorStopped },
		func() error { return nil }, rec)

	for i := 0; i < 3; i++ {
		h.check(context.Background())
	}

	if got := atomic.LoadInt32(&attempts); got != 1 {
		t.Fatalf("restart attempts = %d, want 1 — a refusal must not be retried per probe", got)
	}
	got := h.Snapshot()
	if !strings.Contains(got.Reason, "given up") {
		t.Fatalf("reason = %q, want it to say the supervisor has given up restarting the service", got.Reason)
	}
	if rec.count("service.restarted") != 0 {
		t.Fatal("a refused restart must not be reported as a restart")
	}
}

// TestStuckReasonNamesWhatWasAlreadyTried pins the sentence itself.
//
// ⚠️ Remove this and `stuck` degenerates into "something broke", which sends a
// person to look for a cause that has already been ruled out. The whole value
// of counting is that the message can say "restarting twice did not fix this".
func TestStuckReasonNamesWhatWasAlreadyTried(t *testing.T) {
	r := stuckReason(7, 1, true, false)
	if !strings.Contains(r, "analysis service") || !strings.Contains(r, "daemon") {
		t.Fatalf("stuck reason = %q, want it to name BOTH restarts that were already tried", r)
	}
	// The ladder itself is spent — but the sentence must not claim nothing will
	// happen, because the supervisor now keeps trying on its own (see
	// supervisor_rest_test.go). "Nothing further will be restarted" was true
	// under permanent surrender and is a lie under rest-and-retry.
	if !strings.Contains(r, "keeps retrying") {
		t.Fatalf("stuck reason = %q, want it to say the service keeps retrying on its own", r)
	}
	if strings.Contains(r, "Nothing further") {
		t.Fatalf("stuck reason = %q claims nothing further will happen; the supervisor retries on its own", r)
	}
}

// TestProbingNeverBlocksAReader.
//
// ⚠️ This codebase has a documented incident of exactly the opposite shape:
// warmGate exists because a readiness check performed inline at a call site
// cost thousands of loopback connects per deferred job, and (against a service
// that accepts TCP but never answers) a full client timeout on every one. The
// page's /v1/ledger poll and the restart route must never wait on a probe, so
// the reader path is a mutex read of the last result and nothing else.
func TestProbingNeverBlocksAReader(t *testing.T) {
	release := make(chan struct{})
	p := &probeStub{answers: []bool{true}, block: release}
	h := newTestHealth(t, p.probe, func() error { return nil }, func() error { return nil }, nil)

	probing := make(chan struct{})
	go func() { close(probing); h.check(context.Background()) }()
	<-probing

	done := make(chan serviceWire, 1)
	go func() { done <- h.Snapshot() }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		close(release)
		t.Fatal("Snapshot blocked while a probe was in flight — the reader path must never wait on I/O")
	}
	close(release)
}

// TestTheHealthStripAndTheServiceBlockCannotContradictEachOther.
//
// ⚠️ Remove this and the page can show a green "Analysis service" pill in the
// Today strip beside a `stuck` service block — two statements about one thing,
// disagreeing at exactly the moment a person is looking because something is
// wrong. They are one probe read twice; this is what holds them to that. The
// one legitimate divergence is version skew (answering, but the wrong build),
// which is a different question and carries its own detail.
func TestTheHealthStripAndTheServiceBlockCannotContradictEachOther(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	oldProbe := sidecarProbe.Load()
	oldOwner := currentServiceHealth.Load()
	oldCLI := version.CLI
	t.Cleanup(func() {
		sidecarProbe.Store(oldProbe)
		currentServiceHealth.Store(oldOwner)
		version.CLI = oldCLI
	})
	version.CLI = "2.5.0"

	// The probe the strip would fall back to says HEALTHY, so if the strip ever
	// dialled for itself this test would pass for the wrong reason.
	setSidecarProbe(&sidecarHealthProbe{
		Healthy: func(context.Context) bool { return true },
		Version: func() (string, bool) { return "2.5.0", true },
	})

	for _, tc := range []struct {
		name       string
		answers    []bool
		probes     int
		wantState  serviceState
		wantStatus ledger.Status
	}{
		{"answering", []bool{true}, 1, serviceOK, ledger.StatusOK},
		{"one miss", []bool{false}, 1, serviceDegraded, ledger.StatusFailed},
		{"restarting", []bool{false}, 3, serviceRestarting, ledger.StatusFailed},
		{"stuck", []bool{false}, 9, serviceStuck, ledger.StatusFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newServiceHealth((&probeStub{answers: tc.answers}).probe, "",
				func() error { return nil }, func() error { return nil }, nil, nil)
			currentServiceHealth.Store(h)
			for i := 0; i < tc.probes; i++ {
				h.check(context.Background())
			}
			if got := h.Snapshot().State; got != string(tc.wantState) {
				t.Fatalf("service.state = %q, want %q", got, tc.wantState)
			}

			v := &v3{ledger: ledger.New(), atlasOn: false}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			startHealth(ctx, v, nil, false)
			snap, _ := v.ledger.Read(time.Time{}, 10)
			row := healthByKey(snap)["sidecar"]
			if row.Status != string(tc.wantStatus) {
				t.Fatalf("health[sidecar].status = %q while service.state = %q — the strip and the service block must not disagree",
					row.Status, tc.wantState)
			}
		})
	}
}

// TestTheReportedFailureCountIsTheCounterTheRungsFireOn.
//
// ⚠️ Remove this and someone adds a second, display-only tally that drifts from
// the one that decides — at which point the page reports a number that does not
// explain the actions the machine took, which is worse than reporting none.
func TestTheReportedFailureCountIsTheCounterTheRungsFireOn(t *testing.T) {
	fired := 0
	h := newTestHealth(t, (&probeStub{answers: []bool{false}}).probe,
		func() error { fired++; return nil }, func() error { return nil }, nil)

	for i := 1; i <= serviceRestartSidecarAt; i++ {
		h.check(context.Background())
		if got := h.Snapshot().Failures; got != i {
			t.Fatalf("after %d failures the reported count is %d; it must be the consecutive count, nothing else", i, got)
		}
	}
	if fired != 1 {
		t.Fatalf("the sidecar rung fired %d times; it must fire when the REPORTED count reaches %d", fired, serviceRestartSidecarAt)
	}
}

// TestTheStartupGraceSuppressesEscalationButNotReporting.
//
// ⚠️ Remove this and the two halves of the grace get conflated again. Waiting
// to PROBE leaves the page with no answer for the first minute of every daemon
// (and lets the strip's live fallback disagree with it); not waiting to COUNT
// restarts a cold-starting sidecar for still binding its port.
func TestTheStartupGraceSuppressesEscalationButNotReporting(t *testing.T) {
	var restarts int32
	h := newTestHealth(t, (&probeStub{answers: []bool{false}}).probe,
		func() error { atomic.AddInt32(&restarts, 1); return nil },
		func() error { return nil }, nil)

	for i := 0; i < 5; i++ {
		h.checkMode(context.Background(), false) // inside the grace
	}
	if got := atomic.LoadInt32(&restarts); got != 0 {
		t.Fatalf("sidecar restarts = %d inside the startup grace, want 0", got)
	}
	got := h.Snapshot()
	if got.State != string(serviceDegraded) {
		t.Fatalf("state = %q inside the grace after failed probes, want %q — the page must not be blank", got.State, serviceDegraded)
	}
	if got.Failures != 0 {
		t.Fatalf("failures = %d inside the grace, want 0 — no streak has started", got.Failures)
	}
	if _, known := h.Healthy(); !known {
		t.Fatal("the strip must be able to read an answer during the grace; a probe ran")
	}
}

// TestSupervisorRestartRequestIsNotCountedAsACrash.
//
// ⚠️ Remove this and a health-driven restart consumes the Supervisor's crash
// budget: three deliberate restarts of a WEDGED (but alive) sidecar would trip
// maxRestarts and make the supervisor surrender — the detector causing the
// exact surrender it was built to escape.
func TestSupervisorRestartRequestIsNotCountedAsACrash(t *testing.T) {
	var spawns int32
	s := NewSupervisor(func(int) (*exec.Cmd, error) {
		atomic.AddInt32(&spawns, 1)
		return exec.Command("sleep", "30"), nil
	}, 0, func() bool { return true }, 2*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Start(ctx)

	waitFor(t, 2*time.Second, func() bool { return s.Ready() && s.Pid() != 0 })

	// FOUR deliberate restarts — one more than maxRestarts (3). A crash-counted
	// restart would have surrendered by now.
	for i := 0; i < 4; i++ {
		before := s.Pid()
		if err := s.RequestRestart(); err != nil {
			t.Fatalf("RequestRestart %d: %v", i, err)
		}
		waitFor(t, 10*time.Second, func() bool {
			pid := s.Pid()
			return pid != 0 && pid != before
		})
	}

	if s.FellBack() {
		t.Fatal("four DELIBERATE restarts must not exhaust the CRASH budget — a wedged sidecar would otherwise become an unrestartable one")
	}
	if got := atomic.LoadInt32(&spawns); got < 5 {
		t.Fatalf("spawns = %d, want at least 5 (the original plus four restarts)", got)
	}
}

// TestRequestRestartRefusesWhenTheSupervisorIsNotRunning.
//
// ⚠️ Remove this and the health owner cannot tell "restart issued" from
// "nothing will ever start a sidecar again", which is the difference between a
// `restarting` and a `stuck` on the page — the only two states a person acts on
// differently.
//
// "Not running" means exactly two things now: Start has not begun, or the
// daemon is shutting down. Exceeding the restart cap used to be a third — the
// supervisor surrendered and Start returned — and this test waited for that.
// It rests instead (supervisor_rest_test.go), and a request during the rest is
// ACCEPTED: that is the Restart button working on the one morning it matters.
func TestRequestRestartRefusesWhenTheSupervisorIsNotRunning(t *testing.T) {
	s := NewSupervisor(func(int) (*exec.Cmd, error) { return exec.Command("sleep", "30"), nil },
		0, func() bool { return false }, 100*time.Millisecond)
	s.restBase, s.restMax = time.Hour, time.Hour

	if err := s.RequestRestart(); !errors.Is(err, ErrSupervisorStopped) {
		t.Fatalf("RequestRestart before Start = %v, want ErrSupervisorStopped", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go s.Start(ctx)
	// Never healthy, so it exhausts the fast retries and rests.
	waitFor(t, 6*time.Second, func() bool { return s.FellBack() })
	if err := s.RequestRestart(); err != nil {
		t.Fatalf("RequestRestart while resting = %v, want it accepted — the rest is the state a person presses the button in", err)
	}

	cancel()
	waitFor(t, 6*time.Second, func() bool { return s.AwaitStopped(10 * time.Millisecond) })
	if err := s.RequestRestart(); !errors.Is(err, ErrSupervisorStopped) {
		t.Fatalf("RequestRestart after shutdown = %v, want ErrSupervisorStopped", err)
	}
}
