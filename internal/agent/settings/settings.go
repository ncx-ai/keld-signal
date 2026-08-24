// Package settings holds daemon settings loaded at startup from
// ~/.keld/agent-config.json. Absent/unreadable/invalid file -> zero-value
// defaults. This local file is the seam a future org-level remote control-plane
// plugs into (push settings to all org daemons).
package settings

import (
	"encoding/json"
	"os"
	"strings"

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

	// PIIRegions selects which COUNTRY TIERS of checksum-validated PII
	// recognizers the sidecar runs, on top of the universal ones (card, email,
	// phone, IBAN, crypto wallet). See sidecar/app/pii.py's REGION_RECOGNIZERS
	// for the codes; nil means the default, `us`.
	//
	// Region-scoped rather than all-on because almost every national-id
	// recognizer is a bare digit run plus ONE check digit, so its shapes collide
	// with other countries' — a valid US NPI is ten digits starting 1 or 2,
	// which is exactly the UK NHS number shape, and uk_nhs rolls up to `phi`.
	// Running a country an org has no business in manufactures the most severe
	// class out of ordinary identifiers.
	//
	// Unlike MLBackend this is NOT startup-only: it is shaped local-then-remote
	// like IncludeEntityText (Remote.PIIRegions -> Live.PIIRegions), and rides
	// each /pii request, so an org changing it takes effect on the next prompt
	// rather than the next restart. Local precedence is
	// KELD_PII_REGIONS > agent-config.json > `us`, and remote wins over all
	// three when the key is present.
	PIIRegions []string `json:"pii_regions"`
}

// PIIRegionsEnv overrides the config file's pii_regions. Comma-separated
// ("us,uk"); the literal "none" means the universal tier only, which an empty
// string cannot express because an empty string is "unset".
const PIIRegionsEnv = "KELD_PII_REGIONS"

// DefaultPIIRegions is what a daemon runs when nothing says otherwise.
var DefaultPIIRegions = []string{"us"}

// Regions returns the effective local region list: KELD_PII_REGIONS if set,
// else the config file's value, else DefaultPIIRegions. Always normalized.
//
// A non-nil empty result is meaningful and is preserved: "universal tier only".
func (s Settings) Regions() []string {
	if raw, ok := os.LookupEnv(PIIRegionsEnv); ok && strings.TrimSpace(raw) != "" {
		return NormalizeRegions(strings.Split(raw, ","))
	}
	if s.PIIRegions == nil {
		return append([]string(nil), DefaultPIIRegions...)
	}
	return NormalizeRegions(s.PIIRegions)
}

// NormalizeRegions lowercases, trims, drops empties and dedupes, preserving
// order. The sentinel "none" (from KELD_PII_REGIONS, which cannot express an
// empty list otherwise) yields an empty slice.
//
// It deliberately does NOT validate the codes against a list of known regions.
// The sidecar owns that list (sidecar/app/pii.py REGION_RECOGNIZERS) and ignores
// what it does not recognise; a second copy here would be one more thing to keep
// in step, and would turn a forward-compatible org setting into a client-side
// rejection.
func NormalizeRegions(in []string) []string {
	out := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, r := range in {
		code := strings.ToLower(strings.TrimSpace(r))
		if code == "" || code == "none" || seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	return out
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
