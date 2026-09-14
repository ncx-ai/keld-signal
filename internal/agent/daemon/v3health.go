package daemon

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// sidecarProbe is how the health strip reaches the sidecar client.
//
// A package-level atomic rather than a parameter threaded through
// sidecarService's five-value signature — the same shape blockAdvance and
// tickObserver already use, and for the same reason: the service is
// constructed before Run has anything to hand it, and the probe is absent on
// every machine with no sidecar installed.
var sidecarProbe atomic.Pointer[sidecarHealthProbe]

// sidecarHealthProbe is what the daemon can ask of the analysis service: is it
// answering, what version is it, and — since the health owner arrived — start
// it again.
//
// ⚠️ **ITS PRESENCE IS THE not_applicable TEST.** A nil probe means no analysis
// service exists this daemon run (no sidecar binary installed, its port could
// not be allocated, or enrichment is off) — the state daemon.go deliberately
// runs in rather than treats as a failure. Everything that could restart
// something keys off this being non-nil, so a machine with no sidecar is
// structurally unable to be restarted for not having one.
type sidecarHealthProbe struct {
	// Healthy takes a context so the caller owns the deadline. It used to be a
	// bare func() bool closed over the daemon's context, which was fine for a
	// strip refresh and wrong for a health ladder: a probe with no deadline of
	// its own cannot distinguish "answered no" from "never answered".
	Healthy func(context.Context) bool
	Version func() (string, bool)
	// Restart is Supervisor.RequestRestart — non-blocking, and errors with
	// ErrSupervisorStopped once the supervisor has surrendered.
	Restart func() error
}

// healthRefresh is set by startHealth so that installing the sidecar probe can
// re-read the strip at once instead of waiting for the next tick.
var healthRefresh atomic.Pointer[func()]

// setSidecarProbe publishes the probe AND refreshes the health strip.
//
// ⚠️ **The refresh is the point, not a nicety.** The sidecar is spawned and
// supervised asynchronously, so at the moment startHealth first runs there is
// no probe at all and the strip records the analysis service as "not
// installed". Without this the strip stayed wrong until the next tick — long
// enough that a person opening the page right after login sees a warning about
// a service that is already up, which is the same "confident wrong answer"
// this page exists to remove.
func setSidecarProbe(p *sidecarHealthProbe) {
	sidecarProbe.Store(p)
	if f := healthRefresh.Load(); f != nil {
		(*f)()
	}
}

// startHealth keeps the page's health strip current.
//
// ⚠️ **Every fact here was already known to the daemon and kept to itself.**
// The daemon has always known its own version, whether the sidecar answers,
// whether the sidecar's version matches its own, when telemetry last forwarded
// and what Atlas last said. None of it was reachable from outside the process,
// which is why a machine could publish nothing for 24 days while every check a
// person could run reported no problems. This writes them down.
//
// ⚠️ **A fact that cannot be determined is not written.** An absent health row
// renders as unknown on the page, never as healthy and never as broken — the
// same absent-is-not-false rule the block cells follow. In particular a
// version comparison where either side reads "dev" is NOT skew: a source
// checkout and a local build both report that, and a check that fires on every
// developer machine is one nobody reads on the machine that matters.
func startHealth(ctx context.Context, sig *v3, telemetryLast func() time.Time, atlasOn bool) {
	if sig == nil {
		return
	}
	note := func() {
		var sidecarHealthy func() bool
		var sidecarVersion func() (string, bool)
		if p := sidecarProbe.Load(); p != nil {
			sidecarVersion = p.Version
			// ⚠️ **THE `sidecar` ROW AND THE LEDGER'S `service` BLOCK ARE ONE
			// FACT, READ TWICE — NEVER TWO PROBES.** The page renders this row
			// as "Analysis service" and the `service` block as the restart
			// control's state, so two independent probers would let a machine
			// show a green Analysis-service pill beside a `stuck` service block
			// at precisely the moment someone is looking because something is
			// wrong. A page that contradicts itself is worse than either
			// statement alone. serviceHealth is the SINGLE prober; this is a
			// mutex read of its last result, and the mapping is total:
			//
			//	owner ok                        → StatusOK
			//	owner degraded/restarting/stuck → StatusFailed + sidecar_down
			//	owner not_applicable            → StatusNA (the nil-probe branch
			//	                                  above, never this call)
			//
			// The ONE place they legitimately differ is version skew: an
			// answering-but-outdated sidecar is `ok` to the service block (it
			// is running) and `failed`/`sidecar_outdated` here (it is the wrong
			// build). Different questions, and the detail says which.
			//
			// known=false is the only fallback, and it is a sub-second window
			// before the owner's first probe returns — the owner probes from
			// t=0 and only its COUNTER waits out the startup grace, which is
			// exactly so this fallback is not the startup answer.
			sidecarHealthy = func() bool {
				if ok, known := currentServiceHealth.Load().Healthy(); known {
					return ok
				}
				return p.Healthy(ctx)
			}
		}
		sig.noteHealth(ledger.HealthDaemon, ledger.StatusOK, version.CLI)

		if sidecarHealthy == nil {
			// No sidecar is INSTALLED, which is a different fact from one that
			// is installed and silent: the first is a machine that was never
			// given the analysis service, the second is a machine whose service
			// is down. Only the second is a problem to report.
			sig.noteHealth(ledger.HealthSidecar, ledger.StatusNA, "")
		} else if !sidecarHealthy() {
			sig.noteHealth(ledger.HealthSidecar, ledger.StatusFailed, string(ledger.ReasonSidecarDown))
		} else {
			detail := ""
			if sidecarVersion != nil {
				if v, known := sidecarVersion(); known {
					detail = v
				}
			}
			status := ledger.StatusOK
			if detail != "" && version.CLI != "" {
				if s, known := version.Skew(version.CLI, detail); known && s {
					status = ledger.StatusFailed
					detail = string(ledger.ReasonSidecarOutdated)
				}
			}
			sig.noteHealth(ledger.HealthSidecar, status, detail)
		}

		if telemetryLast != nil {
			if t := telemetryLast(); !t.IsZero() {
				sig.noteHealth(ledger.HealthTelemetry, ledger.StatusOK, "")
			}
			// A zero instant means "nothing has been forwarded yet", which on a
			// fresh install is normal and on a busy machine is a problem, and
			// this cannot tell them apart. So it says nothing, and the page
			// shows the cell as unknown rather than inventing a verdict.
		}

		if !atlasOn {
			sig.noteHealth(ledger.HealthAtlas, ledger.StatusNA, string(ledger.ReasonAtlasOff))
		} else if sig.atlas != nil {
			// ⚠️ **"REACHABLE" AND "NEVER TRIED" ARE DIFFERENT FACTS, and until
			// this the health strip had no `atlas` row at all when Atlas was
			// on — a real end-to-end run against a live Atlas found the page
			// could not tell "Atlas reachable" from "never tried". A zero
			// instant is the second: absent, not healthy and not broken, the
			// same rule telemetryLast follows just above. A non-zero instant
			// with status 0 is a real fact THOUGH — it means a call was made
			// and got no usable answer (a network fault, or an intercepted
			// 2xx) — so it reads as failed, not as unknown.
			if status, at := sig.atlas.LastResponse(); !at.IsZero() {
				if status >= 200 && status < 300 {
					sig.noteHealth(ledger.HealthAtlas, ledger.StatusOK, "")
				} else {
					sig.noteHealth(ledger.HealthAtlas, ledger.StatusFailed, string(classifyAtlasStatus(status)))
				}
			}
		}
		sig.noteHealth(ledger.HealthStore, ledger.StatusOK, "")
	}

	healthRefresh.Store(&note)
	note()
	go func() {
		// ⚠️ **10 seconds, not 30.** This is the strip a person reads to decide
		// whether to restart something or report it, so a stale answer is worse
		// than a slow page: for half a minute it could say the analysis service
		// was down when it had already come up. Every reading is a loopback
		// call or a value the daemon already holds, so the cost is nil.
		t := time.NewTicker(10 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				note()
			}
		}
	}()
}
