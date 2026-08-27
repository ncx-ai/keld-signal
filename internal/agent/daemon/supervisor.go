package daemon

import (
	"context"
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
	maxRestarts        = 3
	healthPollInterval = 200 * time.Millisecond

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

	ready    atomic.Bool
	fellBack atomic.Bool

	// stopGrace is how long the graceful half of stopChild waits before the
	// forceful half runs. A field rather than a constant so tests can shorten
	// it; production reads it from the environment in NewSupervisor.
	stopGrace time.Duration

	// stopped closes when Start returns, so a shutting-down daemon can wait for
	// the child to actually be reaped instead of racing its own process exit.
	// See AwaitStopped.
	stopped     chan struct{}
	stoppedOnce sync.Once

	mu  sync.Mutex
	cmd *exec.Cmd

	// emitter is optional (set via SetEmitter before Start runs); every emit
	// site below guards it nil so the many existing tests that never call
	// SetEmitter are unaffected.
	emitter *clientevents.Emitter
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
		stopGrace:    stopGraceFromEnv(),
		stopped:      make(chan struct{}),
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

// FellBack reports whether the supervisor gave up waiting for health or
// exhausted its restart budget. There is no fallback backend to switch to.
// Like Ready, this is retained for the supervisor's own liveness/restart
// bookkeeping, not as the Worker's per-job gate — that gate is model warmth
// (see Ready's doc comment). A fallen-back or dead sidecar closes the warmth
// gate indirectly: with no process serving /metrics, client.WorkerReady can't
// reach it and reports not-warm, so jobs queue/spool until the daemon is
// restarted and the sidecar comes up cleanly.
func (s *Supervisor) FellBack() bool { return s.fellBack.Load() }

// Start spawns the sidecar and supervises it. It blocks until ctx is Done.
// Callers should run it in a goroutine.
func (s *Supervisor) Start(ctx context.Context) {
	// Latched via Once because the channel is closed from every return path and
	// the documented contract ("Start must be called once") is not enforced.
	defer s.stoppedOnce.Do(func() { close(s.stopped) })

	restarts := 0
	backoff := 250 * time.Millisecond

	for {
		// Spawn.
		cmd, err := s.spawn(s.port)
		if err != nil {
			log.Printf("supervisor: spawn error: %v", err)
			s.emit("sidecar.unavailable", clientevents.SevError, map[string]any{"error": clientevents.RedactError(err)})
			s.fellBack.Store(true)
			return
		}
		// ⚠️ Set here, not in each spawn func, so no caller can forget it: the
		// group is what stopChild signals, and a child spawned without one
		// silently degrades back to the pid-only kill that leaks gigabytes.
		// Must precede cmd.Start — SysProcAttr is read by the fork.
		setProcessGroup(cmd)

		if err := cmd.Start(); err != nil {
			log.Printf("supervisor: cmd.Start error: %v", err)
			s.emit("sidecar.unavailable", clientevents.SevError, map[string]any{"error": clientevents.RedactError(err)})
			s.fellBack.Store(true)
			return
		}

		s.mu.Lock()
		s.cmd = cmd
		s.mu.Unlock()

		// waitCh closes when the child exits.
		waitCh := make(chan error, 1)
		go func(c *exec.Cmd) {
			waitCh <- c.Wait()
		}(cmd)

		// Poll health until ready or readyTimeout.
		readyDeadline := time.Now().Add(s.readyTimeout)
		ticker := time.NewTicker(healthPollInterval)
		becameReady := false

	pollLoop:
		for {
			select {
			case <-ctx.Done():
				ticker.Stop()
				s.stopChild(waitCh) // also reaps, so no goroutine leak
				return

			case exitErr := <-waitCh:
				ticker.Stop()
				_ = exitErr
				// Child exited before we got ready.
				break pollLoop

			case <-ticker.C:
				if s.health() {
					s.ready.Store(true)
					becameReady = true
					ticker.Stop()
					break pollLoop
				}
				if time.Now().After(readyDeadline) {
					ticker.Stop()
					// readyTimeout elapsed — stop child and fall back.
					s.stopChild(waitCh)
					s.fellBack.Store(true)
					return
				}
			}
		}

		if becameReady {
			// Sidecar is healthy; supervise indefinitely.
			select {
			case <-ctx.Done():
				s.stopChild(waitCh)
				return
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

		restarts++
		if restarts > maxRestarts {
			log.Printf("supervisor: restart cap (%d) exceeded, falling back", maxRestarts)
			s.emit("sidecar.unavailable", clientevents.SevError, map[string]any{"restarts": maxRestarts})
			s.fellBack.Store(true)
			return
		}

		log.Printf("supervisor: child exited (restart %d/%d), retrying in %s", restarts, maxRestarts, backoff)
		s.emit("worker.crash", clientevents.SevWarn, map[string]any{
			"restart":      restarts,
			"max_restarts": maxRestarts,
			"backoff_s":    backoff.Seconds(),
		})

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
	}
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
