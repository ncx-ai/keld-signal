// Package settings holds daemon settings loaded at startup from
// ~/.keld/agent-config.json. Absent/unreadable/invalid file -> zero-value
// defaults. This local file is the seam a future org-level remote control-plane
// plugs into (push settings to all org daemons).
package settings

import (
	"encoding/json"
	"os"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// Settings are the admin-configurable daemon options.
type Settings struct {
	// IncludeEntityText, when true, sends domain-entity surface text to Atlas.
	// Default false (privacy-first). Sensitivity spans are always masked
	// regardless of this setting.
	IncludeEntityText bool `json:"include_entity_text"`
	// MLBackend selects which enrichment facets run:
	//
	//   "auto" (default) — the full facet set, on the GLiNER2 model. Jobs
	//     queue/spool until the model is resident; there is never a
	//     lower-fidelity substitute for a facet the model produces.
	//   "deterministic" — the facets that need no model: credential detection
	//     (pure Go) and the workstream dimensions the sidecar's /analyze
	//     derives from transcript coordinates. The SIDECAR STILL RUNS: it is
	//     the client-side analysis-and-enrichment service in general, and
	//     GLiNER2 is one capability it loads lazily on a first inference this
	//     mode never issues. So the service is started and its window analyzer
	//     wired; only the model is never loaded.
	//   "off" — enrichment is disabled entirely; the /enrich ingress
	//     accepts-and-discards.
	//
	// Local, startup-only — not part of the remote settings doc and never
	// re-read at runtime.
	MLBackend string `json:"ml_backend"`
}

// MLEnabled reports whether the GLiNER2 model may be used. Note this is about
// the MODEL, not the sidecar: "deterministic" still runs the sidecar as the
// analysis service (see MLBackend), it just never asks it for inference.
func (s Settings) MLEnabled() bool { return s.MLBackend != "off" && s.MLBackend != "deterministic" }

// EnrichmentEnabled reports whether the enrichment worker runs at all — true
// for everything but "off".
//
// "deterministic" runs the passes that need no model: credential detection and
// the workstream dimensions /analyze derives from coordinates. That is NOT the
// fallback AGENTS.md forbids — it is a different, smaller set of facets, never
// a lower-fidelity substitute for the model's.
//
// It DOES have a readiness to wait on. The workstream pass is served by the
// sidecar, which this mode starts, so its Worker gate polls that service's
// /health (cached, see daemon.serviceHealthGate). A trivially-true gate would
// publish workstream-less profiles for every job that landed while the service
// was still coming up, silently dropping their dimensions; model warmth would
// be worse still, since the model never loads here and the gate would never
// open. The one exception is a machine where NO service can arrive this daemon
// lifetime — no sidecar binary installed, or its loopback port could not be
// allocated. Waiting buys nothing there, so the gate is trivially true, the
// analyzer nil, and enrichment runs its remaining model-free facets with the
// workstreams pass simply unregistered: a dropped facet, reported as
// pipeline_status "partial".
func (s Settings) EnrichmentEnabled() bool { return s.MLBackend != "off" }

// Load reads ~/.keld/agent-config.json. Missing/unreadable/invalid -> defaults.
func Load() Settings {
	var s Settings
	data, err := os.ReadFile(paths.AgentConfigPath())
	if err != nil {
		return s
	}
	_ = json.Unmarshal(data, &s) // invalid JSON -> keep zero-value defaults
	return s
}
