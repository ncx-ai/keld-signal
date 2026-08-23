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
	// MLBackend selects whether ML enrichment runs: "auto" (enrichment runs on
	// the GLiNER2 sidecar; jobs queue/spool until it is ready — never a
	// deterministic fallback), "deterministic" (the sidecar is never used; only
	// the passes that need no model run — e.g. credential detection, and in
	// future the workstream-dimension pass), or "off" (enrichment is disabled
	// entirely; the /enrich ingress accepts-and-discards). Default auto. Local,
	// startup-only — not part of the remote settings doc and never re-read at
	// runtime.
	MLBackend string `json:"ml_backend"`
}

// MLEnabled reports whether the ML sidecar backend may be used.
func (s Settings) MLEnabled() bool { return s.MLBackend != "off" && s.MLBackend != "deterministic" }

// EnrichmentEnabled reports whether the enrichment worker runs at all.
// "deterministic" runs the passes that need no model — credential detection,
// and in future workstream dimensions from tool inputs — and is NOT the
// fallback AGENTS.md forbids: it always produces a different, smaller set of
// facets, never a lower-fidelity substitute for the model's, and it is never
// health-gated (unlike the sidecar, it has no readiness to wait on).
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
