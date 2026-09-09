package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// This file is the daemon's SERVICE HEALTH OWNER, and it exists because both
// supervisors in this system only ever asked "is the process alive?".
//
// ⚠️ **MEASURED ON A REAL MACHINE, 2026-09-07: the daemon ran for 2h14m with no
// sidecar process at all.** In that window it logged `/projects update failed`
// on every 5-minute settings poll and re-spooled 290 enrichment jobs ("model
// not ready after 2m0s, re-spooled", every ~2 minutes). Not one line said the
// analysis service was not running — every message was a downstream symptom of
// a fact nothing on the machine was asking about. Nothing restarted anything; a
// human eventually noticed and ran `launchctl kickstart -k`.
//
// And on 2026-09-04 the sidecar crashed three times in eleven minutes, the
// Supervisor restarted it three times with backoff, hit maxRestarts and logged
// "restart cap (3) exceeded, falling back". It emitted sidecar.unavailable at
// error severity — and then nothing on the machine would ever start it again,
// and nothing told the person.
//
// So this asks the other question, on a timer, and COUNTS. The counter is the
// whole idea: what reaches a person has to be "restarting twice did not fix
// this", not "something broke". Those are different and much more useful
// statements, and only a counter can tell them apart.
//
// ⚠️ **EVERY RUNG IS BOUNDED, and the daemon-restart rung is bounded ACROSS
// PROCESSES.** An unbounded daemon self-restart rebuilds the crash loop one
// level up at a larger blast radius; this repo has a documented incident of 69
// launchd spawns in 12 minutes from exactly that shape. The bound therefore
// cannot live in memory, because the daemon is a new process afterwards — it
// lives in a marker file, following internal/agent/update's marker exactly.
//
// ⚠️ **AND THE MOST DANGEROUS MISTAKE AVAILABLE HERE IS REPORTING A MACHINE
// WITH NO SIDECAR AS UNHEALTHY.** daemon.go deliberately runs without window
// analysis when no sidecar binary is installed (noAnalysisService): that is a
// dropped facet, reported dropped, not a failure. If this owner probed there it
// would restart every such machine forever. The guard is structural rather than
// a condition to remember: with no analysis service there is no PROBE, and with
// no probe this reports serviceNotApplicable and never restarts anything.

// serviceState is the closed vocabulary the page renders. Adding a value is a
// contract change: the page renders each with its own sentence, and a wire test
// enumerates them.
type serviceState string

const (
	// serviceOK — the last probe was answered.
	serviceOK serviceState = "ok"
	// serviceDegraded — one or two consecutive probes went unanswered. One
	// failure is noise; nothing is restarted here.
	serviceDegraded serviceState = "degraded"
	// serviceRestarting — a restart has been issued and we are waiting to see
	// whether it took.
	serviceRestarting serviceState = "restarting"
	// serviceStuck — restarting the analysis service and then the daemon did
	// not fix it. No further restarts; this is the state a person must read.
	serviceStuck serviceState = "stuck"
	// serviceNotApplicable — there is no analysis service on this machine to
	// watch. Never a problem, never a restart.
	serviceNotApplicable serviceState = "not_applicable"
)

// The ladder. Each is a count of CONSECUTIVE unanswered probes, and each rung
// fires on EQUALITY, not on >=, so an action happens once per failure streak
// rather than once per probe.
const (
	// serviceRestartSidecarAt — three consecutive failures buys a sidecar
	// restart. Below it we only report: a single unanswered probe on a busy
	// machine is noise, and restarting for noise is its own outage.
	serviceRestartSidecarAt = 3
	// serviceRestartDaemonAt — six consecutive failures means the sidecar
	// restart did not take, so the thing that supervises it is what is wrong.
	// launchd/systemd guarantee the daemon comes back.
	serviceRestartDaemonAt = 6
)

// Timings. All overridable so a test can run the ladder in milliseconds; the
// defaults are what ships.
const (
	defaultServiceProbeInterval = 15 * time.Second
	defaultServiceProbeTimeout  = 5 * time.Second

	// defaultServiceHealthGrace is how long after startup the first probe
	// waits.
	//
	// ⚠️ It is not politeness — without it the ladder fires on a COLD START. A
	// frozen PyInstaller sidecar needs real seconds to bind its port (the
	// Supervisor gives it a 30s readyTimeout for that reason), so probing from
	// t=0 at 15s intervals would reach the sidecar-restart rung at 45s on a
	// machine that was merely still booting — turning a slow start into a
	// restart loop. 60s sits past the supervisor's own ready budget, after
	// which "not answering" means what it says.
	defaultServiceHealthGrace = 60 * time.Second

	// defaultDaemonRestartCooldown bounds the daemon-restart rung across
	// processes: at most one per window, whatever happens in between.
	//
	// ⚠️ A PERMANENT LATCH WAS THE OTHER OPTION AND IS WORSE. A machine that
	// got stuck once in March would then never be able to self-heal in April,
	// and the marker would have to be cleared by hand — which is the thing this
	// whole file exists to remove. A cooldown is the bound: four self-restarts
	// a day maximum, which cannot become a loop, and a real recovery is not
	// punished forever.
	defaultDaemonRestartCooldown = 6 * time.Hour
)

// serviceHealthMarker is the on-disk record at ~/.keld/state/service-health.json.
//
// ⚠️ **THIS FILE IS THE ONLY REASON THE DAEMON-RESTART RUNG IS BOUNDED.** After
// service.Restart() the daemon is a NEW PROCESS: every counter, latch and
// atomic in this package is gone, and a purely in-memory "we already tried
// that" would be true for exactly as long as it takes to be useless. The shape
// is internal/agent/update's State marker — written BEFORE the restart, read at
// the next start — because that subsystem solves the identical problem for the
// identical reason.
//
// It carries no path, no text and no identifier: an instant, a count and this
// daemon's own version.
type serviceHealthMarker struct {
	// DaemonRestartedAt is the instant of the last daemon restart THIS owner
	// issued. It is deliberately NOT cleared on recovery — the cooldown is
	// measured from it, so clearing it would let a flapping service (fail six,
	// restart, answer once, fail six again) restart the daemon in a loop while
	// every individual decision looked correct.
	DaemonRestartedAt time.Time `json:"daemon_restarted_at"`
	// Failures is the consecutive count at the moment of that restart, kept for
	// diagnosis only.
	Failures int `json:"failures,omitempty"`
	// Version is the daemon version that issued it, kept for diagnosis only.
	Version string `json:"version,omitempty"`
}

func serviceHealthMarkerPath() string {
	return filepath.Join(paths.StateDir(), "service-health.json")
}

// loadServiceHealthMarker reads the marker. A missing or unreadable file is a
// zero marker: unreadable is treated as absent deliberately, because the
// alternative — refusing to act on a file we cannot parse — would make a single
// corrupt byte permanently disable self-healing. The cooldown check below is
// what keeps that safe: a zero instant simply means the cooldown has elapsed,
// and the WRITE is what must succeed before a restart happens (see
// recordDaemonRestart).
func loadServiceHealthMarker(path string) serviceHealthMarker {
	b, err := os.ReadFile(path)
	if err != nil {
		return serviceHealthMarker{}
	}
	var m serviceHealthMarker
	if json.Unmarshal(b, &m) != nil {
		return serviceHealthMarker{}
	}
	return m
}

// serviceHealth is the health owner: one timer, one counter, one ladder.
type serviceHealth struct {
	// probe answers "is the analysis service working right now". NIL means
	// there is no analysis service on this machine — see the not-applicable
	// warning in this file's header. It is given a deadline by the caller.
	probe func(context.Context) bool
	// naReason is what to say when probe is nil. "Enrichment is off" and "no
	// sidecar is installed" are both not-applicable and are different facts.
	naReason string

	// restartSidecar is Supervisor.RequestRestart — non-blocking, and errors
	// with ErrSupervisorStopped once the supervisor has surrendered.
	restartSidecar func() error
	// restartDaemon is service.Restart, the same mechanism auto-update uses.
	restartDaemon func() error

	markerPath       string
	interval         time.Duration
	timeout          time.Duration
	grace            time.Duration
	restartCooldown  time.Duration
	emitFn           func(code string, sev clientevents.Severity, fields map[string]any)
	emitExemptFn     func(code string, sev clientevents.Severity, fields map[string]any)
	now              func() time.Time
	daemonRestarting func() // test seam: called instead of sleeping after a daemon restart

	mu    sync.Mutex
	fails int
	// probed latches on the first completed check. Until then this owner has no
	// answer, which is not the same as a negative one — see Healthy.
	probed          bool
	state           serviceState
	reason          string
	sidecarRestarts int
	gaveUp          bool // the supervisor refused a restart: it has surrendered
	marker          serviceHealthMarker
}

// currentServiceHealth is how the loopback routes reach the owner.
//
// A package-level atomic rather than a value threaded through Run's wiring —
// the same shape sidecarProbe, blockAdvance and tickObserver already use, and
// for the same reason: the routes are built BEFORE wireEnrichment has decided
// whether there is a sidecar at all, and on an unconfigured machine (the
// onboarding handler) they are built when there will never be one.
var currentServiceHealth atomic.Pointer[serviceHealth]

func setServiceHealth(h *serviceHealth) { currentServiceHealth.Store(h) }

// newServiceHealth builds the owner and loads the cross-process restart marker.
func newServiceHealth(
	probe func(context.Context) bool,
	naReason string,
	restartSidecar func() error,
	restartDaemon func() error,
	emit func(string, clientevents.Severity, map[string]any),
	emitExempt func(string, clientevents.Severity, map[string]any),
) *serviceHealth {
	h := &serviceHealth{
		probe:           probe,
		naReason:        naReason,
		restartSidecar:  restartSidecar,
		restartDaemon:   restartDaemon,
		markerPath:      serviceHealthMarkerPath(),
		interval:        envDuration("KELD_SERVICE_HEALTH_INTERVAL", defaultServiceProbeInterval),
		timeout:         envDuration("KELD_SERVICE_HEALTH_TIMEOUT", defaultServiceProbeTimeout),
		grace:           envDuration("KELD_SERVICE_HEALTH_GRACE", defaultServiceHealthGrace),
		restartCooldown: envDuration("KELD_SERVICE_HEALTH_RESTART_COOLDOWN", defaultDaemonRestartCooldown),
		emitFn:          emit,
		emitExemptFn:    emitExempt,
		now:             time.Now,
	}
	h.marker = loadServiceHealthMarker(h.markerPath)
	if probe == nil {
		h.state, h.reason = serviceNotApplicable, naReason
	} else {
		// ⚠️ NOT serviceOK. Nothing has been probed yet, and a start-up default
		// of "ok" is a confident answer from a check that did not run — the one
		// reading this whole file exists to remove. Degraded-with-zero-failures
		// reads as "not established yet" on the page and cannot be mistaken for
		// a verdict.
		h.state, h.reason = serviceDegraded, "the analysis service has not answered yet."
	}
	return h
}

// run drives the ladder until ctx is cancelled. Intended for its own goroutine.
//
// ⚠️ **THE PROBE IS HERE AND NOWHERE ELSE, AND IT IS A TIMER, NOT A GATE.**
// warmGate's doc comment records what happened the last time a readiness check
// was performed inline at a call site: the Worker consults its gate at the top
// of every job and waitWarm re-consults it roughly every 20ms, so probing
// inside the gate meant thousands of loopback requests per deferred job — and,
// against a service that accepts TCP but never answers, the client's full
// timeout on every one. Nothing here is on any caller's path: the routes read a
// mutex-guarded snapshot, and this loop is the only thing that ever dials.
func (h *serviceHealth) run(ctx context.Context) {
	if h.probe == nil {
		// Nothing to watch and nothing that could ever appear this daemon
		// lifetime. The state was set at construction and is already correct.
		return
	}
	start := h.now()
	t := time.NewTicker(h.interval)
	defer t.Stop()
	last := h.now().Round(0) // wall clock; see the sleep detector below
	for {
		// ⚠️ **A LAPTOP THAT SLEPT MUST NOT BE JUDGED ON WHAT IT MISSED.**
		// Timers do not fire while the machine is suspended, and nothing in
		// this daemon or the watcher noticed a wake before this. Two things go
		// wrong without it, and the second is the expensive one. A probe fired
		// in the first moments after a lid opens is measuring a machine whose
		// network stack is still coming back and whose sidecar may have been
		// suspended mid-request, so it fails for reasons that say nothing
		// about the service. And a streak carried ACROSS the sleep is a
		// judgement about a machine that no longer exists — three failures at
		// 23:00 plus one at 08:00 is not four consecutive failures, it is one,
		// and treating it as four restarts a daemon on the strength of
		// evidence from last night.
		//
		// The detector is the gap itself. This loop wakes on a ticker, so on
		// an ordinary tick the wall clock advances by about one interval; a
		// jump far beyond that means time passed while nothing ran. The
		// threshold is deliberately generous — a heavily loaded machine can
		// delay a tick by seconds, and mistaking load for sleep would reset a
		// real failure streak and hide exactly the outage this owner exists to
		// catch. Erring the other way costs one extra probe cycle.
		//
		// A backwards jump counts too: a clock corrected by NTP, or a VM
		// restored from a snapshot, leaves the same "the interval I measured
		// is meaningless" state.
		//
		// ⚠️ **WALL CLOCK, DELIBERATELY — `Round(0)` STRIPS THE MONOTONIC
		// READING, AND WITHOUT THAT THIS DETECTOR NEVER FIRED.** Two
		// `time.Now()` values carry a monotonic reading and `Sub` prefers it,
		// and on macOS Go's monotonic clock does not advance while the machine
		// sleeps. So a night of sleep measured as a few seconds of gap, the
		// streak carried across every dark wake, and this line was logged
		// exactly zero times on the night of 2026-09-08/09 while `pmset -g log`
		// shows the machine asleep for hours. The jump a sleep makes is visible
		// only on the wall clock, which is the one thing this comparison exists
		// to read. `start` keeps its monotonic reading on purpose: the grace it
		// measures is awake time since the wake, which is what a cold start
		// needs.
		if gap := h.now().Round(0).Sub(last); gap > h.sleepGap() || gap < 0 {
			h.wokeUp(gap)
			start = h.now() // re-arm the startup grace: a wake is a cold start
		}
		last = h.now().Round(0)
		// ⚠️ **THE GRACE SUPPRESSES ESCALATION, NOT REPORTING, and the split
		// matters on every single daemon start.** Waiting to probe at all left
		// this owner with no answer for the first minute, while the page's
		// health strip went on probing live — so the strip could show the
		// analysis service green while the `service` block said it had not
		// answered. A page that contradicts itself is worse than either
		// statement alone, and it would have done so at startup on every
		// machine. So the probe runs from t=0 and only the COUNTER waits.
		h.checkMode(ctx, h.now().Sub(start) >= h.grace)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// check runs one probe and advances the ladder. The counting form; see
// checkMode for the startup form.
func (h *serviceHealth) check(ctx context.Context) { h.checkMode(ctx, true) }

// checkMode runs one probe. counting=false is the startup grace: the state is
// recorded so the page is never blank or wrong, but no failure is counted, so
// a sidecar that is merely still binding its port cannot be restarted for it.
func (h *serviceHealth) checkMode(ctx context.Context, counting bool) {
	pctx, cancel := context.WithTimeout(ctx, h.timeout)
	ok := h.probe(pctx)
	cancel()
	h.mu.Lock()
	h.probed = true
	h.mu.Unlock()
	switch {
	case ok:
		h.onSuccess()
	case counting:
		h.onFailure()
	default:
		// Degraded with a failure count of ZERO, deliberately: `failures` is
		// the consecutive count the 3-and-6 rungs fire on, and no streak has
		// started yet. The reason is what carries the difference.
		h.setState(serviceDegraded, "the analysis service has not answered yet; it may still be starting.")
	}
}

// onSuccess resets the counter.
//
// ⚠️ **THE RESET IS LOAD-BEARING.** Without it the count is not "consecutive"
// but "ever", so it accumulates across hours of ordinary operation and
// eventually crosses a rung on a machine whose service is fine — a self-restart
// caused entirely by the detector.
// sleepGap is how large a gap between ticks has to be before this owner calls
// it a suspended machine rather than a slow one.
//
// Four intervals, floored at two minutes. Both halves earn their place: the
// multiple keeps it meaningful if someone sets a long interval, and the floor
// keeps it from being twitchy if someone sets a short one — at a 5s interval,
// four ticks is 20s, which a loaded machine can genuinely lose to scheduling.
// A real sleep is minutes to hours, so nothing is lost by being generous, and
// what is bought is that ordinary load can never silently wipe a failure
// streak.
func (h *serviceHealth) sleepGap() time.Duration {
	if g := 4 * h.interval; g > 2*time.Minute {
		return g
	}
	return 2 * time.Minute
}

// wokeUp discards the pre-sleep failure streak.
//
// ⚠️ **IT RESETS THE COUNTER BUT DOES NOT CLAIM THE SERVICE IS HEALTHY.** The
// state is left exactly as it was, because this owner has not probed since the
// machine came back and does not know — publishing `ok` here would be a
// confident answer from a check that never ran, which is the one thing this
// codebase refuses everywhere else. The very next line of the loop probes, and
// that probe sets the state honestly. What is thrown away is only the arithmetic
// that a suspended machine invalidated.
//
// The restart marker is deliberately NOT cleared: its cooldown is measured in
// wall-clock hours, so sleeping through it is exactly as good as being awake
// through it, and clearing it would let a lid-close reset a bound whose whole
// job is to survive a restart.
func (h *serviceHealth) wokeUp(gap time.Duration) {
	h.mu.Lock()
	prevFails := h.fails
	h.fails = 0
	h.mu.Unlock()

	if prevFails == 0 {
		return // nothing was in flight; the wake cost nothing and needs no line
	}
	log.Printf("keld-agent: %s passed between health checks — the machine was most likely asleep; "+
		"discarding %d failed check(s) from before it, which say nothing about the machine that came back",
		gap.Round(time.Second), prevFails)
	h.emit("service.wake_reset", clientevents.SevInfo, map[string]any{
		"gap_s":            int(gap.Round(time.Second).Seconds()),
		"failures_dropped": prevFails,
	})
}

func (h *serviceHealth) onSuccess() {
	h.mu.Lock()
	prev, prevFails, restarts := h.state, h.fails, h.sidecarRestarts
	h.fails = 0
	h.state, h.reason = serviceOK, ""
	h.gaveUp = false
	h.mu.Unlock()

	if prev != serviceOK && prevFails > 0 {
		log.Printf("keld-agent: the analysis service is answering again after %d failed health check(s)", prevFails)
		// Floor-exempt: under the default warn floor a recovery would be
		// dropped and only failures would ever reach Atlas, leaving "recovered"
		// indistinguishable from "still broken and gone quiet".
		h.emitExempt("service.recovered", clientevents.SevInfo, map[string]any{
			"failures_before":  prevFails,
			"sidecar_restarts": restarts,
		})
	}
}

// onFailure advances the ladder by exactly one rung.
func (h *serviceHealth) onFailure() {
	h.mu.Lock()
	h.fails++
	n := h.fails
	h.mu.Unlock()

	switch {
	case n < serviceRestartSidecarAt:
		h.setState(serviceDegraded, fmt.Sprintf(
			"the analysis service did not answer its health check (%d in a row). Nothing has been restarted — one missed check is usually noise.", n))

	case n == serviceRestartSidecarAt:
		h.restartSidecarRung(n)

	case n < serviceRestartDaemonAt:
		// Deliberately no second restart here: rungs fire on equality so an
		// action happens once per streak, never once per probe.
		//
		// ⚠️ **THIS USED TO SAY "was restarted" UNCONDITIONALLY, AND ON A REAL
		// MACHINE THAT WAS A LIE.** Observed live: the sidecar rung asked the
		// supervisor to restart, the supervisor had already surrendered and
		// refused, and the page then read "the analysis service was restarted
		// and still has not answered" — describing an action that never
		// happened, to a person deciding whether to intervene. The whole point
		// of this ladder is that what reaches a person is an accurate account
		// of what was already tried; a reason string that invents a remedy is
		// the same defect as a button that claims success on a 202.
		h.mu.Lock()
		gaveUp := h.gaveUp
		h.mu.Unlock()
		if gaveUp {
			h.setState(serviceRestarting, fmt.Sprintf(
				"the analysis service is not answering (%d checks in a row) and could not be restarted — "+
					"its supervisor has given up. Restarting Signal itself is the next thing to try.", n))
			break
		}
		h.setState(serviceRestarting, fmt.Sprintf(
			"the analysis service was restarted and still has not answered (%d checks in a row).", n))

	case n == serviceRestartDaemonAt:
		h.restartDaemonRung(n)

	default:
		h.setStuck(n)
	}
}

// restartSidecarRung is rung 3: restart the analysis service, once.
func (h *serviceHealth) restartSidecarRung(n int) {
	if h.restartSidecar == nil {
		h.setState(serviceDegraded, fmt.Sprintf(
			"the analysis service has not answered %d checks in a row and this daemon cannot restart it.", n))
		return
	}
	if err := h.restartSidecar(); err != nil {
		// ErrSupervisorStopped: the restart cap was exceeded and the supervisor
		// surrendered (S5). Nothing on this machine will spawn a sidecar again
		// this daemon lifetime, so the sidecar rung is spent the moment it is
		// reached — but we do NOT skip ahead to the daemon rung. Rungs fire on
		// equality by design; three more probes (45s at the shipped interval)
		// against a 2h14m outage is not the cost worth trading that clarity for.
		h.mu.Lock()
		h.gaveUp = true
		h.mu.Unlock()
		log.Printf("keld-agent: the analysis service has not answered %d checks in a row and its supervisor has given up restarting it: %v", n, err)
		h.setState(serviceDegraded, fmt.Sprintf(
			"the analysis service has not answered %d checks in a row and its supervisor has given up restarting it.", n))
		return
	}
	h.mu.Lock()
	h.sidecarRestarts++
	restarts := h.sidecarRestarts
	h.mu.Unlock()
	log.Printf("keld-agent: the analysis service has not answered %d health checks in a row; restarting it", n)
	h.emit("service.restarted", clientevents.SevWarn, map[string]any{
		"failures": n,
		"restarts": restarts,
		"trigger":  "health",
	})
	h.setState(serviceRestarting, fmt.Sprintf(
		"the analysis service stopped answering after %d checks; it is being restarted.", n))
}

// restartDaemonRung is rung 6: restart the whole daemon, at most once per
// cooldown window, and only if the marker recording that could be WRITTEN.
func (h *serviceHealth) restartDaemonRung(n int) {
	h.mu.Lock()
	last := h.marker.DaemonRestartedAt
	restarts := h.sidecarRestarts
	gaveUp := h.gaveUp
	h.mu.Unlock()

	if h.restartDaemon == nil {
		h.setStuck(n)
		return
	}
	if !last.IsZero() && h.now().Sub(last) < h.restartCooldown {
		// Already spent, and the fact survived the restart because it is on
		// disk. This is the rung that must never loop.
		h.setStuck(n)
		return
	}
	if err := h.recordDaemonRestart(n); err != nil {
		// ⚠️ **A RESTART WE CANNOT RECORD IS A RESTART WE MUST NOT PERFORM.**
		// The marker is the only bound on this rung; without a durable record
		// the next process would find no memory of the attempt and restart
		// again, and again — 69 launchd spawns in 12 minutes is what that looks
		// like. Refusing is the safe direction: the machine stays broken and
		// says so, which is strictly better than a machine that restarts
		// forever and says nothing.
		log.Printf("keld-agent: NOT restarting the daemon — could not record the attempt at %s: %v", h.markerPath, err)
		h.setStuck(n)
		return
	}

	log.Printf("keld-agent: the analysis service has not answered %d health checks in a row and restarting it did not help; restarting the daemon", n)
	h.emit("service.daemon_restart", clientevents.SevError, map[string]any{
		"failures":           n,
		"sidecar_restarts":   restarts,
		"supervisor_gave_up": gaveUp,
	})
	h.setState(serviceRestarting, fmt.Sprintf(
		"the analysis service did not answer %d checks in a row and restarting it did not help; the daemon is restarting.", n))

	if err := h.restartDaemon(); err != nil {
		// The marker STAYS. The restart may still have taken effect from the
		// service manager's point of view, and an un-recorded attempt is the
		// one thing this rung cannot afford. Same refusal
		// update.Confirm makes when its own restart fails.
		log.Printf("keld-agent: daemon restart failed: %v", err)
		h.emit("service.daemon_restart_failed", clientevents.SevError, map[string]any{
			"failures": n,
			"error":    clientevents.RedactError(err),
		})
		h.setStuck(n)
	}
}

// recordDaemonRestart persists the attempt BEFORE it happens.
func (h *serviceHealth) recordDaemonRestart(n int) error {
	m := serviceHealthMarker{DaemonRestartedAt: h.now().UTC(), Failures: n, Version: version.CLI}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(h.markerPath), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(h.markerPath, b, 0o600); err != nil {
		return err
	}
	h.mu.Lock()
	h.marker = m
	h.mu.Unlock()
	return nil
}

// setStuck is the end of the ladder: say what was already TRIED, not that
// something broke.
//
// ⚠️ That distinction is the entire point of counting. "Something broke" sends
// a person to look for a cause that has already been ruled out; "restarting it
// twice did not fix this" tells them the remedy they would have reached for is
// spent, which is a different and much more useful statement.
func (h *serviceHealth) setStuck(n int) {
	h.mu.Lock()
	restarts := h.sidecarRestarts
	daemonRestarted := !h.marker.DaemonRestartedAt.IsZero()
	gaveUp := h.gaveUp
	already := h.state == serviceStuck
	h.mu.Unlock()

	reason := stuckReason(n, restarts, daemonRestarted, gaveUp)
	h.setState(serviceStuck, reason)
	if already {
		return // said once per streak, not once per probe
	}
	log.Printf("keld-agent: %s", reason)
	h.emit("service.stuck", clientevents.SevError, map[string]any{
		"failures":           n,
		"sidecar_restarts":   restarts,
		"daemon_restarted":   daemonRestarted,
		"supervisor_gave_up": gaveUp,
	})
}

// stuckReason is the sentence a person reads. Split out so a test can pin the
// wording against the claim it makes — a `stuck` that does not name what was
// tried is indistinguishable from a `degraded` and is worth nothing.
//
// ⚠️ The tail used to read "Nothing further will be restarted automatically."
// That was true while the supervisor surrendered after its restart cap and is
// a lie now that it rests and retries on its own (supervisor.go,
// afterFailedStart). What is still true, and is what the sentence now says, is
// that THIS ladder is spent: it will not restart the daemon again, and the
// remaining automatic recovery is the supervisor's — or the person's, via
// Restart, which ends a rest immediately.
func stuckReason(n int, sidecarRestarts int, daemonRestarted, gaveUp bool) string {
	const tail = " The daemon will not be restarted again by this check; the service keeps retrying on its own, and Restart tries now."
	switch {
	case sidecarRestarts > 0 && daemonRestarted:
		return fmt.Sprintf("restarted the analysis service and then the daemon; still not answering after %d checks.", n) + tail
	case daemonRestarted:
		return fmt.Sprintf("restarted the daemon; the analysis service is still not answering after %d checks.", n) + tail
	case sidecarRestarts > 0:
		return fmt.Sprintf("restarted the analysis service; it is still not answering after %d checks, and the daemon cannot be restarted again yet.", n) + tail
	case gaveUp:
		return fmt.Sprintf("the analysis service has not answered %d checks in a row, its supervisor is not running, and the daemon cannot be restarted again yet.", n)
	default:
		return fmt.Sprintf("the analysis service has not answered %d checks in a row and could not be restarted.", n) + tail
	}
}

func (h *serviceHealth) setState(s serviceState, reason string) {
	h.mu.Lock()
	h.state, h.reason = s, reason
	h.mu.Unlock()
}

// emit / emitExempt are nil-safe wrappers so every call site above can read as
// a plain statement. emitExempt is the floor-exempt one: a recovery is SevInfo
// and would be dropped by the default `warn` floor, which would leave "it came
// back" indistinguishable from "it is still broken and has gone quiet" — the
// same reason features.encoder_provisioned is floor-exempt.
func (h *serviceHealth) emit(code string, sev clientevents.Severity, fields map[string]any) {
	if h == nil || h.emitFn == nil {
		return
	}
	h.emitFn(code, sev, fields)
}

func (h *serviceHealth) emitExempt(code string, sev clientevents.Severity, fields map[string]any) {
	if h == nil || h.emitExemptFn == nil {
		h.emit(code, sev, fields)
		return
	}
	h.emitExemptFn(code, sev, fields)
}

// Healthy reports the last probe's answer, cached — never a live call. It is
// what v3health.go's strip reads so the daemon holds ONE prober rather than two
// that can disagree.
//
// ⚠️ **known IS THE WHOLE POINT OF THE SECOND RETURN VALUE.** The ladder does
// not probe for its first grace period (60s, so a cold-starting sidecar is not
// restarted for still booting), and during that window this owner has NO answer
// — which is a different fact from "answered no". Returning a bare false there
// would make the page report the analysis service as down for the first minute
// of every daemon's life, which is the confident-wrong-answer failure this
// branch exists to remove. A not-applicable machine also reports known=false;
// the strip's own nil-probe branch is what decides "no sidecar installed", and
// this must never be the thing that says it.
func (h *serviceHealth) Healthy() (ok, known bool) {
	if h == nil {
		return false, false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.probed {
		return false, false
	}
	return h.state == serviceOK, true
}

// Snapshot is what the loopback routes serve. A mutex read, never a probe.
func (h *serviceHealth) Snapshot() serviceWire {
	if h == nil {
		// ⚠️ Reachable in production: the onboarding handler mounts these routes
		// on a machine that has no configuration and therefore no analysis
		// service at all. not_applicable is the honest answer there, and the
		// page renders it as nothing rather than as a problem.
		return serviceWire{State: string(serviceNotApplicable), Reason: "no analysis service is being watched on this daemon run."}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return serviceWire{State: string(h.state), Reason: h.reason, Failures: h.fails}
}

// RestartSidecar is the loopback restart button's action. It returns
// immediately — Supervisor.RequestRestart is a one-slot channel send — and
// reports whether there is anything to restart at all.
func (h *serviceHealth) RestartSidecar() error {
	if h == nil || h.restartSidecar == nil {
		return errNoServiceToRestart
	}
	if err := h.restartSidecar(); err != nil {
		return err
	}
	h.mu.Lock()
	h.sidecarRestarts++
	restarts := h.sidecarRestarts
	h.mu.Unlock()
	h.emit("service.restarted", clientevents.SevWarn, map[string]any{
		"restarts": restarts,
		"trigger":  "manual",
	})
	h.setState(serviceRestarting, "the analysis service is being restarted at your request.")
	return nil
}

// startServiceHealth constructs the owner, publishes it for the routes, and
// runs the ladder on its own goroutine. Called from Run AFTER wireEnrichment,
// which is when sidecarProbe either exists or definitively never will.
func startServiceHealth(ctx context.Context, enrichmentEnabled bool, emitter *clientevents.Emitter) *serviceHealth {
	var probe func(context.Context) bool
	var restart func() error
	if p := sidecarProbe.Load(); p != nil {
		probe, restart = p.Healthy, p.Restart
	}
	na := "no analysis service is installed on this machine, so there is nothing to watch."
	if !enrichmentEnabled {
		na = "enrichment is off, so there is no analysis service to watch."
	}
	var emit, emitExempt func(string, clientevents.Severity, map[string]any)
	if emitter != nil {
		emit, emitExempt = emitter.Emit, emitter.EmitExempt
	}
	h := newServiceHealth(probe, na, restart, serviceRestarter{}.Restart, emit, emitExempt)
	setServiceHealth(h)
	// Refresh the page's health strip now that it can read this owner's cached
	// answer rather than dialling the sidecar itself.
	if f := healthRefresh.Load(); f != nil {
		(*f)()
	}
	go h.run(ctx)
	return h
}
