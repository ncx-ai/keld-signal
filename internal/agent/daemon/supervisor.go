package daemon

import (
	"context"
	"log"
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
)

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
	}
}

// Ready reports whether the sidecar has reported healthy at least once.
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
// exhausted its restart budget. When true, callers should fall back to the
// deterministic model.
func (s *Supervisor) FellBack() bool { return s.fellBack.Load() }

// Start spawns the sidecar and supervises it. It blocks until ctx is Done.
// Callers should run it in a goroutine.
func (s *Supervisor) Start(ctx context.Context) {
	restarts := 0
	backoff := 250 * time.Millisecond

	for {
		// Spawn.
		cmd, err := s.spawn(s.port)
		if err != nil {
			log.Printf("supervisor: spawn error: %v", err)
			s.emit("sidecar.fallback", clientevents.SevError, map[string]any{"error": clientevents.RedactError(err)})
			s.fellBack.Store(true)
			return
		}
		if err := cmd.Start(); err != nil {
			log.Printf("supervisor: cmd.Start error: %v", err)
			s.emit("sidecar.fallback", clientevents.SevError, map[string]any{"error": clientevents.RedactError(err)})
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
				s.killChild()
				<-waitCh // reap to avoid goroutine leak
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
					// readyTimeout elapsed — kill child and fall back.
					s.killChild()
					<-waitCh
					s.fellBack.Store(true)
					return
				}
			}
		}

		if becameReady {
			// Sidecar is healthy; supervise indefinitely.
			select {
			case <-ctx.Done():
				s.killChild()
				<-waitCh
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
			s.emit("sidecar.fallback", clientevents.SevError, map[string]any{"restarts": maxRestarts})
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

// killChild sends SIGKILL to the current child if it exists.
// Called from the supervisor goroutine while holding no locks, or while
// ctx.Done fires — always safe because we hold mu.
func (s *Supervisor) killChild() {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd != nil && cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
