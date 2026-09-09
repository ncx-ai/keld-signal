package daemon

import (
	"context"
	"errors"
	"log"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

const (
	// maxRestarts bounds CONSECUTIVE failed starts before the supervisor rests.
	// It is not a lifetime budget: a child that became healthy resets it (see
	// Start), and exceeding it is a pause, never a surrender.
	maxRestarts        = 3
	healthPollInterval = 200 * time.Millisecond

	// defaultStartSleepGap is the wall-clock jump between two health polls
	// that reads as "the machine was asleep" while a sidecar was starting. The
	// polls are 200ms apart, so 30s is 150 polls' worth — a loaded machine
	// delays a tick by seconds, not by half a minute — and mistaking load for
	// sleep would only ever grant a slow child one more readiness window.
	//
	// ⚠️ **THE READINESS DEADLINE IS MEASURED IN TIME THE MACHINE WAS AWAKE,
	// AND THAT NEEDS THE WALL CLOCK, NOT GO'S MONOTONIC ONE.** Measured on a
	// real Mac (2026-09-09): a 90s deadline was armed at 03:19, the machine
	// then slept with ~2-second dark wakes every 15 minutes, and at 05:20, 07:23
	// and 09:42 — each within three seconds of a dark wake in `pmset -g log` —
	// the supervisor killed a child that had been given seconds of real time as
	// a "failed start". The third exhausted the cap. So the loop compares
	// wall-clock instants (`Round(0)` strips the monotonic reading): a jump far
	// beyond the poll interval is a sleep, and a sleep re-arms the deadline
	// instead of spending it. Go's monotonic clock cannot be the detector here,
	// because on macOS it does not advance while the machine sleeps — which is
	// also why the health owner's own sleep detector, built on it, logged
	// nothing that night.
	defaultStartSleepGap = 30 * time.Second

	// The rest between rounds of fast retries: first a minute, doubling to
	// half an hour. Long enough that a sidecar which is genuinely broken costs
	// a spawn and one loud line per rest rather than a busy loop; short enough
	// that a machine coming back from sleep, a swapped binary or a returned
	// venv is picked up without anyone restarting the daemon. Fields on the
	// Supervisor so tests can shrink them.
	defaultRestBase = time.Minute
	defaultRestMax  = 30 * time.Minute

	// DefaultStopGrace is how long stopChild lets the sidecar shut itself down
	// after SIGTERM before the process group is SIGKILLed.
	//
	// ⚠️ It is a BOUND, not a budget to be spent. Both ends are measured against
	// a real sidecar on this branch:
	//   - idle, the whole teardown takes 110.6 ms from SIGTERM to the process
	//     being gone — so the common case never comes near 5s.
	//   - with the text encoder mid-weights-load, the parent did NOT exit inside
	//     5s and the group SIGKILL reaped it and both children (a 550 MB encoder
	//     child and the multiprocessing resource tracker) with nothing left over.
	// The second is why the bound must NOT track the teardown's worst case:
	// featuretext.TextSource.shutdown drains an in-flight encode with a 30s
	// timeout and one real batch costs ~92s, so a supervisor that waited it out
	// would hang the daemon's whole shutdown on work about to be discarded —
	// and, under launchd, would be SIGKILLed at 20s before it could reap
	// anything at all. 5s is ~45x the measured clean stop; anything slower than
	// that is a wedge, and a wedge gets SIGKILLed.
	//
	// It also has to fit inside the service managers' own stop grace — launchd
	// SIGKILLs at 20s, and a daemon killed there cannot reap anything.
	// Override with KELD_SIDECAR_STOP_GRACE.
	DefaultStopGrace = 5 * time.Second
)

// stopGraceFromEnv resolves the post-SIGTERM grace period (KELD_SIDECAR_STOP_GRACE,
// a Go duration). A non-positive or unparseable value falls back to the default:
// a zero grace would skip the graceful path this fix exists to make reachable.
func stopGraceFromEnv() time.Duration {
	if v := os.Getenv("KELD_SIDECAR_STOP_GRACE"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return DefaultStopGrace
}

// Supervisor spawns and supervises a sidecar child process. It polls a health
// function until the process becomes ready (or a readyTimeout elapses). On
// unexpected child exit it restarts with exponential backoff up to maxRestarts
// times. When ctx is cancelled the child is killed and no restart is attempted.
//
// Concurrency invariants:
//   - ready and fellBack are atomic.Bool — safe to read from any goroutine.
//   - cmd is guarded by mu — the kill path reads cmd under the lock while the
//     spawn path sets it under the same lock.
type Supervisor struct {
	spawn        func(port int) (*exec.Cmd, error)
	port         int
	health       enrich.HealthFunc
	readyTimeout time.Duration

	ready atomic.Bool
	// fellBack is true while there is NO child and the supervisor is between
	// rounds — the fast retries are spent and it is resting before the next
	// attempt. It is a state, not a verdict: cleared the moment a child becomes
	// healthy, and RequestRestart ends the rest early. See FellBack.
	fellBack atomic.Bool

	// now is the clock the readiness deadline and its sleep detector read.
	// time.Now in production; tests substitute one they can jump. Only wall
	// time is ever compared (see defaultStartSleepGap).
	now      func() time.Time
	sleepGap time.Duration
	restBase time.Duration
	restMax  time.Duration

	// stopGrace is how long the graceful half of stopChild waits before the
	// forceful half runs. A field rather than a constant so tests can shorten
	// it; production reads it from the environment in NewSupervisor.
	stopGrace time.Duration

	// stopped closes when Start returns, so a shutting-down daemon can wait for
	// the child to actually be reaped instead of racing its own process exit.
	// See AwaitStopped.
	stopped     chan struct{}
	stoppedOnce sync.Once

	// started latches when Start's loop begins, and restartReq is the 1-slot
	// signal RequestRestart uses to ask that loop to replace the current child.
	// Together they are what makes a DELIBERATE restart distinguishable from a
	// crash: see RequestRestart.
	started    atomic.Bool
	restartReq chan struct{}

	mu  sync.Mutex
	cmd *exec.Cmd

	// emitter is optional (set via SetEmitter before Start runs); every emit
	// site below guards it nil so the many existing tests that never call
	// SetEmitter are unaffected.
	emitter *clientevents.Emitter

	// onRespawn is called (on its own goroutine) each time a REPLACEMENT child
	// becomes healthy — never for the first one. Optional; see SetOnRespawn.
	// Guarded by mu because, unlike emitter, it is legitimately set AFTER
	// Start's goroutine is running: deterministicBackend starts the supervisor
	// before Run has resolved the project list it would re-post.
	onRespawn func()
}

// SetOnRespawn registers a callback fired after a RESTARTED sidecar becomes
// healthy. Call before Start, like SetEmitter.
//
// ⚠️ IT EXISTS BECAUSE A SIDECAR RESTART SILENTLY DESTROYS PARENT-PROCESS
// STATE THE DAEMON THINKS IT STILL HAS. The concrete case is the project
// attribution list: `attribution._projects` lives in the sidecar parent's
// module namespace, so a crash-restart takes it, while the daemon's own
// bookkeeping still records the sidecar as told — and its POST is gated on
// having something NEW to say. The result was permanent: every /attribute
// answered `skipped:no_projects` until the daemon itself restarted. Anything
// else the daemon pushes DOWN to the sidecar once (rather than on every
// request) has the same shape and belongs on this hook.
//
// NOT fired for the first ready: that is startup, which every such pusher
// already handles on its own path. Fired on its own goroutine so a slow
// re-push cannot stall supervision of the child it is re-pushing to.
func (s *Supervisor) SetOnRespawn(f func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onRespawn = f
}

// respawnHook reads the callback under the lock; nil when none is registered.
func (s *Supervisor) respawnHook() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.onRespawn
}

// SetEmitter wires an Emitter so Start's anomaly sites (spawn/start failure,
// restart-cap-exceeded fallback, child crash/retry) also emit client events
// alongside their existing log.Printf. Call before Start; not safe to change
// concurrently with a running Start (matches the one-shot construction
// pattern used elsewhere in the daemon).
func (s *Supervisor) SetEmitter(e *clientevents.Emitter) { s.emitter = e }

// NewSupervisor builds a Supervisor. Start must be called once to begin
// supervision; it blocks until ctx is cancelled.
func NewSupervisor(
	spawn func(port int) (*exec.Cmd, error),
	port int,
	health enrich.HealthFunc,
	readyTimeout time.Duration,
) *Supervisor {
	return &Supervisor{
		spawn:        spawn,
		port:         port,
		health:       health,
		readyTimeout: readyTimeout,
		now:          time.Now,
		sleepGap:     defaultStartSleepGap,
		restBase:     defaultRestBase,
		restMax:      defaultRestMax,
		stopGrace:    stopGraceFromEnv(),
		stopped:      make(chan struct{}),
		restartReq:   make(chan struct{}, 1),
	}
}

// AwaitStopped blocks until Start has returned — i.e. the child and its process
// group have actually been signalled and reaped — or until d elapses. It
// reports whether the supervisor stopped within d.
//
// ⚠️ Without this the whole graceful path is DEAD CODE on the one shutdown that
// matters. Run's serve() returns as soon as ctx is cancelled and the listener
// closes, so the daemon process used to exit within microseconds of cancelling
// the context the supervisor is still reacting to: the SIGTERM was frequently
// never sent, and even the old SIGKILL was a race. A supervisor's kill only
// reaps a tree if the daemon is still alive to do the reaping.
//
// Bounded, because a supervisor that cannot finish must not hold the daemon
// open either — the whole stop path has a hard ceiling of stopGrace plus the
// small change around it.
func (s *Supervisor) AwaitStopped(d time.Duration) bool {
	select {
	case <-s.stopped:
		return true
	case <-time.After(d):
		return false
	}
}

// StopGrace is the supervisor's post-SIGTERM grace period. Exposed so callers
// bounding their own wait on AwaitStopped can size it from the same number
// rather than restating it.
func (s *Supervisor) StopGrace() time.Duration { return s.stopGrace }

// awaitSidecarStop builds serviceFacets.AwaitSidecarStop for a supervisor,
// sizing the bound off that supervisor's own grace rather than a second
// constant that could drift from it. The extra second is slack for the SIGTERM,
// the group sweep and the reap either side of the grace — not more waiting: if
// the supervisor cannot finish in stopGrace+1s it is wedged, and the daemon
// exits anyway rather than hang. nil sup ⇒ nil func, so a run with no
// supervised sidecar waits for nothing.
func awaitSidecarStop(sup *Supervisor) func() {
	if sup == nil {
		return nil
	}
	return func() {
		if !sup.AwaitStopped(sup.StopGrace() + time.Second) {
			log.Printf("supervisor: sidecar stop did not complete within %s; exiting anyway",
				sup.StopGrace()+time.Second)
		}
	}
}

// ErrSupervisorStopped is what RequestRestart returns when there is no
// supervision loop left to ask: Start has not begun, or it has returned because
// the daemon is shutting down. It is not a transient condition, and a caller
// that gets it must ESCALATE rather than retry.
//
// ⚠️ It used to have a third cause — the restart cap exceeded — and that one
// is gone on purpose. Surrender made every transient (a night of macOS dark
// wakes, a slow first spaCy load, a binary swapped mid-update) into a state
// only a human restarting the daemon could leave, and the page's Restart
// button answered "cannot restart" in exactly the moment it was pressed. The
// supervisor now RESTS between rounds and a request during the rest is
// accepted and acted on at once. See Start.
var ErrSupervisorStopped = errors.New("sidecar supervisor is not running")

// RequestRestart asks the supervision loop to replace the current child with a
// fresh one. It NEVER blocks: it drops a token in a one-slot channel and
// returns, so an HTTP handler or a health timer can call it directly. The
// actual stop can take up to stopGrace, and it happens on the supervisor's own
// goroutine.
//
// ⚠️ **A DELIBERATE RESTART IS NOT A CRASH, AND CONFLATING THEM WOULD MAKE THE
// CRASH CAP MEANINGLESS IN BOTH DIRECTIONS.** The loop does not count it
// against maxRestarts — a health owner restarting a WEDGED (but alive) sidecar
// must not consume the budget that exists for one that keeps dying — and
// equally it does not RESET the count, because laundering the crash budget
// through a health-driven restart is how a crash-looping sidecar gets restarted
// forever. The bound on deliberate restarts lives with the caller that issues
// them (serviceHealth's ladder, and the restart route's rate limiter), not here.
//
// Returns ErrSupervisorStopped when Start has not begun or has already
// returned; a second call while one is already queued is a no-op, since one
// pending restart is all a one-slot channel can mean.
func (s *Supervisor) RequestRestart() error {
	if !s.started.Load() {
		return ErrSupervisorStopped
	}
	select {
	case <-s.stopped:
		return ErrSupervisorStopped
	default:
	}
	select {
	case s.restartReq <- struct{}{}:
	default: // one already queued; asking twice means the same thing as once
	}
	return nil
}

// Ready reports whether the sidecar has reported healthy at least once. This
// is latched liveness for the supervisor's own restart/backoff machinery — it
// is NOT the Worker's per-job readiness gate. That gate is model warmth (see
// mlBackendWithOpts's warmGate, driven by client.WorkerReady polling the
// sidecar's /metrics endpoint), which is non-latching and tracks whether the
// model is resident right now.
func (s *Supervisor) Ready() bool { return s.ready.Load() }

// Pid returns the PID of the current child process, or 0 if no child is
// running. Safe to call from any goroutine (protected by mu).
func (s *Supervisor) Pid() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Pid
	}
	return 0
}

// FellBack reports whether the supervisor is RESTING: the fast retries are
// spent, there is no child, and the next attempt is a bounded pause away (or
// one RequestRestart away). There is no fallback backend to switch to, and the
// state is not permanent — it clears when a child becomes healthy.
// Like Ready, this is retained for the supervisor's own liveness/restart
// bookkeeping, not as the Worker's per-job gate — that gate is model warmth
// (see Ready's doc comment). A resting or dead sidecar closes the warmth gate
// indirectly: with no process serving /metrics, client.WorkerReady can't reach
// it and reports not-warm, so jobs queue/spool until the sidecar comes up.
func (s *Supervisor) FellBack() bool { return s.fellBack.Load() }

// Start spawns the sidecar and supervises it. It blocks until ctx is Done.
// Callers should run it in a goroutine.
func (s *Supervisor) Start(ctx context.Context) {
	// Latched via Once because the channel is closed from every return path and
	// the documented contract ("Start must be called once") is not enforced.
	defer s.stoppedOnce.Do(func() { close(s.stopped) })
	s.started.Store(true)

	// restarts counts CONSECUTIVE failed starts — a crash, a readiness timeout,
	// a spawn that could not happen — and is reset by a child that answers
	// /health. backoff is the fast retry pause while that budget lasts; rest is
	// the long one once it is spent. All three start over on ready.
	restarts := 0
	backoff := 250 * time.Millisecond
	rest := s.restBase
	spawns := 0

	for {
		// deliberate marks THIS iteration's child as having been replaced on
		// request rather than having died. Reset per spawn, so a request can
		// never leak into the accounting of a later, genuine crash.
		deliberate := false
		// failure names how this iteration's child ended, for the retry log
		// line; the crash case is the default and the others overwrite it.
		failure := "child exited"
		spawns++
		// No child is healthy until this iteration's child answers /health.
		// Ready used to latch true for the supervisor's life, so a reader saw
		// "ready" across a crash, a restart and the whole readiness wait of the
		// replacement — a stale yes about a process that no longer existed.
		s.ready.Store(false)

		// Spawn. A spawn that cannot happen is a failed start like any other,
		// not the end of supervision: the binary may be mid-swap by an update
		// or about to be fetched by onboarding, and the daemon should not need
		// restarting to notice it arrive.
		cmd, err := s.spawn(s.port)
		if err != nil {
			log.Printf("supervisor: spawn error: %v", err)
			s.emit("sidecar.unavailable", clientevents.SevError, map[string]any{"error": clientevents.RedactError(err)})
			if !s.afterFailedStart(ctx, "the sidecar could not be spawned", &restarts, &backoff, &rest) {
				return
			}
			continue
		}
		// ⚠️ Set here, not in each spawn func, so no caller can forget it: the
		// group is what stopChild signals, and a child spawned without one
		// silently degrades back to the pid-only kill that leaks gigabytes.
		// Must precede cmd.Start — SysProcAttr is read by the fork.
		setProcessGroup(cmd)

		if err := cmd.Start(); err != nil {
			log.Printf("supervisor: cmd.Start error: %v", err)
			s.emit("sidecar.unavailable", clientevents.SevError, map[string]any{"error": clientevents.RedactError(err)})
			if !s.afterFailedStart(ctx, "the sidecar could not be started", &restarts, &backoff, &rest) {
				return
			}
			continue
		}

		s.mu.Lock()
		s.cmd = cmd
		s.mu.Unlock()

		// waitCh closes when the child exits.
		waitCh := make(chan error, 1)
		go func(c *exec.Cmd) {
			waitCh <- c.Wait()
		}(cmd)

		// Poll health until ready or readyTimeout — a timeout measured in time
		// the machine was AWAKE. Wall-clock instants only (Round(0) strips the
		// monotonic reading), because the sleep detector below needs to SEE the
		// jump a sleep makes, and on macOS Go's monotonic clock does not make
		// one. See defaultStartSleepGap for the night that proved it.
		wall := func() time.Time { return s.now().Round(0) }
		lastPoll := wall()
		readyDeadline := lastPoll.Add(s.readyTimeout)
		ticker := time.NewTicker(healthPollInterval)
		becameReady := false

	pollLoop:
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				s.stopChild(waitCh) // also reaps, so no goroutine leak
				return

			case <-s.restartReq:
				// Asked to replace a child that has not become healthy yet.
				// Honoured, deliberately: "spawned but never answered /health"
				// is the exact state that stranded a machine for 2h14m, and it
				// is invisible to the crash path because nothing exited.
				ticker.Stop()
				s.stopChild(waitCh)
				deliberate = true
				break pollLoop

			case exitErr := <-waitCh:
				ticker.Stop()
				_ = exitErr
				// Child exited before we got ready.
				break pollLoop

			case <-ticker.C:
				now := wall()
				// A gap far beyond the poll interval is time the machine did not
				// run — asleep, or a clock stepped backwards. The child got none
				// of it, so the deadline is re-armed rather than spent: a wake is
				// a cold start. Same rule serviceHealth applies to its counter.
				if gap := now.Sub(lastPoll); gap > s.sleepGap || gap < 0 {
					log.Printf("supervisor: %s passed between two health polls while the sidecar was starting — "+
						"the machine was most likely asleep; giving it a fresh %s to answer", gap.Round(time.Second), s.readyTimeout)
					s.emit("service.wake_reset", clientevents.SevInfo, map[string]any{
						"gap_s":  int(gap.Round(time.Second).Seconds()),
						"during": "sidecar_start",
					})
					readyDeadline = now.Add(s.readyTimeout)
				}
				lastPoll = now
				if s.health() {
					s.ready.Store(true)
					becameReady = true
					ticker.Stop()
					break pollLoop
				}
				if now.After(readyDeadline) {
					ticker.Stop()
					// ⚠️ **THIS PATH GAVE UP FOR THE DAEMON'S WHOLE LIFE, ON
					// ONE SLOW START, AND SAID NOTHING AT ALL.** It killed the
					// child, latched fellBack and returned — so `Start` was
					// over, `RequestRestart` answered ErrSupervisorStopped
					// forever, and not one line was logged or emitted. Measured
					// on a real machine: the sidecar was spawned, lived ~30s,
					// was killed here, and the daemon then ran with no analysis
					// service while reporting only downstream symptoms.
					//
					// Two things were wrong. It was SILENT, which is why the
					// state could persist for hours unexplained. And it was
					// TERMINAL ON THE FIRST TIMEOUT, which is not what a
					// readiness deadline means: a child that is merely slow to
					// load — spaCy is ~619 MB and the text encoder more —
					// deserves the same three attempts a crashing one gets. A
					// child that exits is retried; a child that is slow was
					// not, and the slow case is the more recoverable of the
					// two.
					//
					// So it falls through to the retry path, counting against
					// maxRestarts like any other failure — and since the cap is
					// now a rest rather than a surrender, three slow starts cost
					// a pause, not the daemon's lifetime.
					s.stopChild(waitCh)
					log.Printf("supervisor: the sidecar did not answer /health within %s of awake time; "+
						"treating it as a failed start", s.readyTimeout)
					s.emit("sidecar.slow_start", clientevents.SevWarn, map[string]any{
						"ready_timeout_s": int(s.readyTimeout.Seconds()),
						"restart":         restarts + 1,
						"max_restarts":    maxRestarts,
					})
					failure = "the sidecar did not become ready"
					break pollLoop
				}
			}
		}

		if becameReady && spawns > 1 {
			// A REPLACEMENT child is healthy. Whatever the daemon pushed down
			// to the previous process's memory went with it — see
			// SetOnRespawn. Own goroutine: this does network I/O against the
			// child we are supervising. Keyed on "not the first child" rather
			// than on the crash counter, which a healthy child now resets — and
			// which a DELIBERATE restart never incremented, so the hook used to
			// skip exactly the restarts the health owner issues.
			if hook := s.respawnHook(); hook != nil {
				go hook()
			}
		}
		if becameReady {
			// A child that answered /health is not a failed start. The cap
			// bounds CONSECUTIVE failures, so the budget, the fast backoff and
			// the rest all start over — four crashes over a month of uptime are
			// four recoveries, not a crash loop.
			s.fellBack.Store(false)
			restarts, backoff, rest = 0, 250*time.Millisecond, s.restBase
			// Sidecar is healthy; supervise indefinitely.
			select {
			case <-ctx.Done():
				s.stopChild(waitCh)
				return
			case <-s.restartReq:
				// The healthy-then-wedged case: the process is alive, so the
				// supervisor has nothing to react to. /health is the only thing
				// that knows, which is why this signal exists at all.
				s.stopChild(waitCh)
				deliberate = true
			case <-waitCh:
				// Child died after becoming ready.
			}
		}

		// Decide whether to restart.
		select {
		case <-ctx.Done():
			return
		default:
		}

		if deliberate {
			// Not a crash: no restarts++, no cap check, and the backoff is
			// reset because there is nothing to back off FROM — this child was
			// stopped on purpose, at a moment the caller chose.
			log.Printf("supervisor: sidecar restart requested; respawning")
			s.emit("service.restarted", clientevents.SevWarn, map[string]any{"requested": true})
			backoff = 250 * time.Millisecond
			continue
		}

		if !s.afterFailedStart(ctx, failure, &restarts, &backoff, &rest) {
			return
		}
	}
}

// afterFailedStart accounts for one failed start — a crash, a readiness
// timeout, a spawn that could not happen — and waits before the caller tries
// again: the fast backoff while the consecutive-failure budget lasts, then a
// REST once it is spent. It reports false only when ctx ended during the wait.
//
// ⚠️ **EXCEEDING THE CAP USED TO RETURN FROM Start, AND THAT RETURN IS THE
// DEFECT THIS FUNCTION REPLACES.** Surrender turned every transient into a
// state only a human could leave: measured on 2026-09-09, a night of macOS
// dark wakes spent the three attempts on a sidecar that had been given seconds
// of real time, `RequestRestart` then refused for the rest of the daemon's
// life, and the page's Restart button answered "cannot restart" to the person
// pressing it. AGENTS.md had carried it as a known gap — "a service that
// starts and then permanently gives up still wedges this mode".
//
// A rest is bounded and loud: one `sidecar.unavailable` naming when the next
// attempt is, a pause that doubles to restMax, and a restart request (the
// button, the health owner) ends it immediately. A sidecar that is genuinely
// broken therefore costs one spawn and one line per half hour, which is the
// price of never needing a human to notice that a venv came back.
func (s *Supervisor) afterFailedStart(ctx context.Context, what string, restarts *int, backoff, rest *time.Duration) bool {
	*restarts++
	if *restarts <= maxRestarts {
		log.Printf("supervisor: %s (restart %d/%d), retrying in %s", what, *restarts, maxRestarts, *backoff)
		s.emit("worker.crash", clientevents.SevWarn, map[string]any{
			"restart":      *restarts,
			"max_restarts": maxRestarts,
			"backoff_s":    backoff.Seconds(),
		})
		select {
		case <-ctx.Done():
			return false
		case <-time.After(*backoff):
		}
		*backoff *= 2
		return true
	}

	log.Printf("supervisor: restart cap (%d) exceeded; no analysis service until the next attempt in %s "+
		"(a restart request tries now)", maxRestarts, *rest)
	s.emit("sidecar.unavailable", clientevents.SevError, map[string]any{
		"restarts":   maxRestarts,
		"retry_in_s": int(rest.Seconds()),
	})
	s.fellBack.Store(true)
	select {
	case <-ctx.Done():
		return false
	case <-s.restartReq:
		log.Printf("supervisor: restart requested during the rest; trying now")
	case <-time.After(*rest):
		log.Printf("supervisor: rest over; trying to start the sidecar again")
	}
	*restarts = 0
	*backoff = 250 * time.Millisecond
	*rest *= 2
	if *rest > s.restMax {
		*rest = s.restMax
	}
	return true
}

// emit is a nil-safe convenience over s.emitter (optional — see SetEmitter).
func (s *Supervisor) emit(code string, sev clientevents.Severity, fields map[string]any) {
	if s.emitter != nil {
		s.emitter.Emit(code, sev, fields)
	}
}

// stopChild terminates the current child AND every descendant it spawned, then
// drains waitCh so the Wait goroutine never leaks. Called only from the
// supervisor goroutine, which is the sole reader of waitCh.
//
// ⚠️ THE OLD VERSION OF THIS FUNCTION WAS THE LEAK. It called
// cmd.Process.Kill() — SIGKILL, to the sidecar's PID and nothing else. SIGKILL
// cannot be caught, so the sidecar's lifespan teardown (which does call
// wm.shutdown() and _TEXT_SOURCE.shutdown(), correctly) never ran, and its
// multiprocessing children — the ~2.9 GB GLiNER2 worker and the ~1.7-2.3 GB
// text encoder — were reparented to init/systemd and held that memory until
// the machine was rebooted. Observed live on a dev machine: encoder children
// and GLiNER2 workers sitting at ppid 1.
//
// Two halves, and BOTH are load-bearing:
//
//  1. GRACEFUL. SIGTERM the sidecar alone. This is the only way its existing,
//     already-correct teardown ever executes, and it is what makes the children
//     exit cleanly rather than being shot. Not sent to the group — see
//     terminateChild for why killing the children out from under the teardown
//     is worse than letting it do its job.
//  2. FORCEFUL, and UNCONDITIONAL. SIGKILL the whole process group afterwards,
//     whether the graceful path succeeded, timed out, or was never available
//     (Windows). A parent that exited cleanly but left a straggler is still
//     swept; against an empty group this is a no-op ESRCH. Running it even on
//     the happy path is the difference between "we asked nicely" and "no
//     survivors", and the latter is the actual requirement.
//
// The grace period is HARD-BOUNDED (stopGrace, default 5s): a wedged child is
// killed, never waited on. A supervisor that hangs is its own failure.
func (s *Supervisor) stopChild(waitCh <-chan error) {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		// Nothing was ever started; there is no waitCh producer to drain
		// either, so returning here preserves the old no-child behaviour.
		return
	}
	pid := cmd.Process.Pid

	// Resolve the group BEFORE signalling, while the child is certainly alive:
	// once cmd.Wait() reaps it the pid is no longer a reliable handle, and
	// childGroup's pgid == pid guard is what stops us signalling the daemon's
	// own group by mistake.
	pgid, group := childGroup(pid)

	drained := false
	if gracefulStopSupported() {
		if err := terminateChild(pid); err == nil {
			select {
			case <-waitCh:
				drained = true
			case <-time.After(s.stopGrace):
				log.Printf("supervisor: sidecar pid %d did not exit %s after SIGTERM; killing its process group",
					pid, s.stopGrace)
			}
		}
	}

	_ = killProcessTree(pid, pgid, group)

	if !drained {
		<-waitCh
	}
}
