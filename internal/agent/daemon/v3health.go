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

// sidecarHealthProbe is the two questions the strip asks: is it answering, and
// what version is it.
type sidecarHealthProbe struct {
	Healthy func() bool
	Version func() (string, bool)
}

func setSidecarProbe(p *sidecarHealthProbe) { sidecarProbe.Store(p) }

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
			sidecarHealthy, sidecarVersion = p.Healthy, p.Version
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
		}
		sig.noteHealth(ledger.HealthStore, ledger.StatusOK, "")
	}

	note()
	go func() {
		t := time.NewTicker(30 * time.Second)
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
