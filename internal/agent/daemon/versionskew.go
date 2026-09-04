package daemon

import (
	"context"
	"log"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// THE VERSION HANDSHAKE BETWEEN THE TWO HALVES OF AN INSTALL.
//
// ⚠️ THEY SHIP SEPARATELY AND NOTHING COMPARED THEM. `keld-agent` and the frozen
// analysis sidecar are different artifacts on different cadences: the macOS pkg
// cannot carry the sidecar past Apple's notarization (~15k files put a real
// submission 4+ hours into an unbounded queue), so `onboard.command` fetches it
// separately — and until this file neither half knew what the other was.
// Measured on a real machine: a 2.3.0 daemon ran ~3 weeks against an Aug 11
// sidecar that had no `/blocks` route at all. It answered 404, the emitter read
// that as "no blocks closed yet", and the machine published telemetry and ZERO
// blocks while `keld signal doctor` reported no problems. See
// docs/superpowers/specs/2026-09-04-sidecar-version-skew-discovery.md.
//
// ⚠️ THIS IS THE GENERAL DETECTOR AND THE PER-ROUTE ONES ARE NOT. The block
// emitter's own missing-route line and `/attribute`'s RouteUnsupported each
// catch ONE route going missing, after the fact, only for a machine doing that
// kind of work. A version comparison catches the whole class — every route
// added since, and every route added next — at startup, on every machine,
// whether or not it ever cuts a block.
//
// It runs on ALL THREE OSes, unlike the installer half of the fix: Windows
// bundles the sidecar in the Inno payload and Linux's install.sh replaces it
// unconditionally, so only the macOS pkg can produce skew — but a hand-placed
// sidecar, an interrupted update or a `.prev` restored by hand can produce it
// anywhere, and a detector that only ran where the known cause lives would miss
// exactly the unknown ones.

// versionProbe is the capability this needs from the sidecar client: one
// /health read. An interface so the reporter is testable without a live
// sidecar, mirroring how the daemon declares windowAnalyzer / blockDigesterCap.
type versionProbe interface {
	Health(ctx context.Context) (sidecar.HealthReport, bool)
}

const (
	// How often to ask while the sidecar is still coming up. The freeze is a
	// ~15,000-file tree, so a cold start is seconds, not milliseconds; polling
	// faster buys nothing and this is the least urgent thing the daemon does.
	versionProbeInterval = 3 * time.Second
	// After this, give up SILENTLY. A sidecar that never answers is a different
	// problem with its own report (`sidecar.unavailable`, emitted by the
	// supervisor), and adding a second event for it would describe one failure
	// twice under two names. Generous because a first spawn contends with the
	// model-weight fetch on a fresh install.
	versionProbeDeadline = 10 * time.Minute
)

// reportSidecarVersionSkew waits for the sidecar to answer, then says ONCE
// whether the two halves disagree.
//
// Once per daemon run, by construction: it returns after the first conclusive
// answer. A respawned sidecar cannot change the answer — it is the same binary
// on disk — so re-checking on the supervisor's respawn hook would only produce
// duplicate reports of one fact.
//
// SILENT when it cannot tell. `version.Skew` returns known=false if either half
// reports "dev" (a source checkout, `make sidecar`'s venv wrapper, a local
// freeze) or nothing at all, and a developer machine must never be told it has
// a problem it does not have. That is the same refusal `localagent.ModelState`
// and `TelemetryState` make.
func reportSidecarVersionSkew(ctx context.Context, probe versionProbe, emitter *clientevents.Emitter) {
	if probe == nil {
		return
	}
	deadline := time.NewTimer(versionProbeDeadline)
	defer deadline.Stop()
	tick := time.NewTicker(versionProbeInterval)
	defer tick.Stop()

	for {
		if h, ok := probe.Health(ctx); ok {
			noteSidecarVersion(h.Version, emitter)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-tick.C:
		}
	}
}

// noteSidecarVersion is the decision, split out so a test can drive it with a
// fixed pair rather than a live probe.
func noteSidecarVersion(sidecarVersion string, emitter *clientevents.Emitter) {
	skewed, known := version.Skew(version.CLI, sidecarVersion)
	if !known || !skewed {
		return
	}
	// The remedy names the INSTALLER, not the daemon. Restarting the daemon
	// fixes nothing here — the wrong artifact is on disk — and a line that
	// suggests it sends people round a loop that cannot terminate.
	log.Printf("keld-agent: VERSION SKEW — this agent is %s and the analysis sidecar is %s. "+
		"They ship as separate artifacts, so a route this agent needs may not exist in that "+
		"sidecar and the work would go missing SILENTLY. Re-run the Keld installer "+
		"(macOS: /usr/local/keld/onboard.command) to bring the sidecar to %s.",
		version.CLI, sidecarVersion, version.CLI)
	if emitter != nil {
		// Floor-exempt for the reason the lifecycle events are: it describes what
		// this run IS, not something that went wrong during it, and it must reach
		// the fleet view from a machine whose severity floor is set high.
		emitter.EmitExempt("sidecar.version_skew", clientevents.SevWarn, map[string]any{
			"daemon":  version.CLI,
			"sidecar": sidecarVersion,
		})
	}
}
