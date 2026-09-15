package integrations

import "github.com/ncx-ai/keld-signal/internal/config"

// Snapshot reads every fact from disk and computes one state per catalogue
// entry, in catalogue order.
//
// ⚠️ THIS IS THE ONLY WAY ANY CONSUMER GETS STATES. The loopback route, `keld
// signal doctor`, `keld signal status` and the client-events emitter all call
// it; it calls Compute, which decides. That is AC-8 expressed as a call graph
// rather than as a convention — doctor and the pane cannot disagree, because
// neither of them decides anything.
//
// It performs no network call, triggers no model load and starts no download.
func Snapshot(d Deps, opts Options) Response {
	d = d.withDefaults()
	if d.Manifest == nil {
		// A manifest that will not load is an EMPTY manifest, not a failure:
		// every tool then reads `not_configured`, which is what a machine with
		// no manifest actually is.
		if m, err := config.LoadManifest(); err == nil {
			d.Manifest = m
		} else {
			d.Manifest = &config.Manifest{Tools: map[string]config.ToolManifest{}}
		}
	}
	if d.Lanes == nil {
		d.Lanes = LoadLanes()
	}

	now := d.Now().UTC()
	facts := make(map[string]Facts, len(Catalogue))
	for _, e := range Catalogue {
		facts[e.ID] = Facts{
			Configured:  Configured(e, d.Manifest),
			Wiring:      ReadWiring(e, d),
			Lanes:       ReadLanes(e, d),
			ToolVersion: ToolVersion(e, d),
			BackupPath:  BackupPath(e, d.Manifest),
		}
	}
	return Respond(now, Compute(now, Catalogue, facts, opts), opts.AutoSetup)
}

// StateOf is the one row for one id, for a caller that has an id in hand
// (the setup and report routes). Absent when the id is not in the catalogue.
func StateOf(id string, r Response) (Integration, bool) {
	for _, in := range r.Integrations {
		if in.ID == id {
			return in, true
		}
	}
	return Integration{}, false
}

// SetupResult is POST /v1/integrations/{id}/setup's body.
type SetupResult struct {
	// Backup is where the previous config went, "" when there was none to
	// keep (a tool with no config file yet).
	Backup string `json:"backup"`
	// RestartRequired is always true on a successful apply: a tool reads its
	// telemetry configuration once, at startup, so one already running keeps
	// posting wherever it was pointed when it launched. Nothing on the machine
	// can detect or fix that from outside — measured, a session started before
	// setup emitted 0 telemetry events over 11 hours while its blocks
	// published normally.
	RestartRequired bool `json:"restart_required"`
	// Conflict names the tool's own section that stopped the apply, "" when
	// there was none. A conflict is REPORTED, never resolved by replacing:
	// the person owns that file.
	Conflict string `json:"conflict,omitempty"`
}
