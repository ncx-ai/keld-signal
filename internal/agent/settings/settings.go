// Package settings holds daemon settings loaded at startup from
// ~/.keld/agent-config.json. Absent/unreadable/invalid file -> zero-value
// defaults. This local file is the seam a future org-level remote control-plane
// plugs into (push settings to all org daemons).
package settings

import (
	"encoding/json"
	"os"
	"strconv"
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

	// Features and FeaturesPublish are THE SIGNAL-EMBEDDINGS PATH's two local
	// toggles: collecting the vectors, and sending them to Atlas. BOTH DEFAULT
	// OFF — the zero value is the default, which is why these are plain bools
	// where PIIRegions is a slice: "absent" and "false" are the same answer
	// here, and the org override is expressed with pointers on Remote.Features
	// instead.
	//
	// Shaped local-then-remote like IncludeEntityText, and read per sweep
	// rather than at startup, so an org flipping either takes effect on the
	// next sweep rather than the next restart.
	//
	// ⚠️ The third toggle in the design, `capture`, is deliberately absent: it
	// is the sidecar's KELD_CAPTURE and is fingerprinted into `parse_state`, so
	// flipping it forces a reparse. See settings/features.go.
	Features        bool `json:"features"`
	FeaturesPublish bool `json:"features_publish"`

	// Blocks is THE V2 PATH's toggle: whether the block emitter runs at all.
	// Local, read at startup, default OFF — the zero value is the default, same
	// as Features.
	//
	// ⚠️ It exists because KELD_BLOCKS alone is unreachable from an installer:
	// no service definition on any OS carries an environment block. A v2
	// install writes this key; KELD_BLOCKS still overrides it either way. See
	// blocks.Enabled.
	//
	// NO REMOTE OVERRIDE, deliberately. Remote.Features governs the
	// signal-embeddings toggles; blocks has no equivalent and adding one is a
	// separate decision with Atlas-side work. The asymmetry is real and worth
	// knowing: an org can turn feature rows off fleet-wide and cannot turn
	// blocks off.
	Blocks bool `json:"blocks"`

	// Attribution is THE PROJECT ATTRIBUTION PATH's toggle: whether the
	// daemon schedules/drives on-device block-to-project matching at all.
	// Local, read at startup, default OFF — the zero value is the default,
	// same shape as Blocks (and for the same reason: an env-only knob is
	// unreachable from an installer). KELD_ATTRIBUTION still overrides it
	// either way — see attrib.Enabled.
	Attribution bool `json:"attribution"`

	// SendToAtlas is THE BOUNDARY between Signal and the Atlas connector
	// (docs/v3/contracts.md, deliverable D4). nil/absent means ON — Atlas is
	// the default for an installed daemon — and false means the connector
	// package is never constructed: no publish, no settings poll, no
	// workstreams sync, no code redemption; zero outbound connections.
	// A pointer so an absent key stays distinguishable from an explicit false,
	// the same idiom Remote.PIIRegions uses. KELD_ATLAS=0 overrides to off,
	// KELD_ATLAS=1 to on. Written by the page's Settings pane.
	SendToAtlas *bool `json:"send_to_atlas,omitempty"`

	// DevBlocks is the DEVELOPER block granularity: "" (default, the cutter's
	// 20-minute cap / 15-minute idle), "prompt" (one block per human prompt),
	// "bin" (one block per non-empty 5-minute bin) or "minute" (60-second bins
	// against a separate refseries-dev.db). Anything but "" is REFUSED while
	// SendToAtlas is on — a minute-long block is not a fact about anyone's work
	// and must never reach the org's numbers; the validation lives in
	// DevBlocksMode, so a caller cannot read an unsafe value. KELD_DEV_BLOCKS
	// overrides. Forwarded to the sidecar as KELD_DEV_BLOCKS at spawn.
	DevBlocks string `json:"dev_blocks,omitempty"`

	// DevGenerate puts a "Generate block" button in the page's top bar.
	//
	// ⚠️ **UNLIKE DevBlocks, IT IS NOT REFUSED WHILE SendToAtlas IS ON, AND THE
	// DIFFERENCE IS DELIBERATE.** A dev granularity MISLABELS real work — a
	// minute-long block is a false statement about something a person actually
	// did — so it must never reach the org's numbers. The generator instead ADDS
	// work that never happened, and reaching Atlas is the whole point of it:
	// what a developer is testing is that a block travels the entire path, and a
	// generator that stopped at the ledger would prove only that the ledger
	// works. What makes that acceptable is that every generated session is named
	// `devgen-…` (devgen.SessionPrefix), so the rows are filterable and
	// deletable wherever they land, rather than indistinguishable from real
	// spend forever.
	DevGenerate bool `json:"dev_generate,omitempty"`

	// DevRepos is the repository list the generator draws from. Empty means the
	// three stable defaults carried in the embedded corpus, so the button works
	// before anyone configures anything.
	//
	// The generator also offers a fourth, RANDOM repository that no project rule
	// can match. That is not padding: every declared repo attributes cleanly and
	// therefore tests half the product, and the pane that lists work with
	// nowhere to put it can only be exercised by evidence that genuinely matches
	// nothing.
	DevRepos []string `json:"dev_repos,omitempty"`

	// ShowBreaks is a page preference: lay the gaps between focus blocks
	// between the cards. Local, harmless, here because the page's Settings pane
	// writes this file and a preference with no home is a preference that
	// resets.
	ShowBreaks bool `json:"show_breaks,omitempty"`

	// WorkstreamsOff lists workstream keys whose values are EXCLUDED from
	// attribution on this machine ("counts for my work" switched off). Local
	// only; Atlas is never told. A developer's work can then never land in the
	// Marketing bucket.
	WorkstreamsOff []string `json:"workstreams_off,omitempty"`

	// TelemetryPort is the loopback port AI tools POST OTLP to.
	//
	// ⚠️ IT HAS A CONFIG KEY FOR THE REASON `Blocks` DOES: an env-only knob is
	// unreachable from an installed daemon. No service definition on any OS
	// carries an environment block — LaunchAgentPlist and SystemdUnit have none,
	// the Windows task is a bare /TR "<exe>" run — so an operator told to "set
	// KELD_TELEMETRY_PORT" by a bind-failure message would have nowhere to set it.
	//
	// It must also be readable by the CLI: `keld signal setup` writes this address
	// into tool configs, and the two halves must agree on the number. Zero means
	// unset; KELD_TELEMETRY_PORT still wins.
	TelemetryPort int `json:"telemetry_port,omitempty"`
}

// TelemetryPortEnv overrides the configured telemetry port.
const TelemetryPortEnv = "KELD_TELEMETRY_PORT"

// TelemetryPortOrDefault resolves env > agent-config.json > def.
//
// An out-of-range value is REFUSED rather than clamped: binding 0 would hand the
// daemon an ephemeral port, which is exactly the staleness a fixed port exists to
// prevent — tool configs are written once and must stay valid.
func (s Settings) TelemetryPortOrDefault(def int) int {
	if n, ok := validPort(os.Getenv(TelemetryPortEnv)); ok {
		return n
	}
	if s.TelemetryPort > 0 && s.TelemetryPort < 65536 {
		return s.TelemetryPort
	}
	return def
}

func validPort(v string) (int, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n <= 0 || n >= 65536 {
		return 0, false
	}
	return n, true
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
