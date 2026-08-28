package settings

// Features is the org-governed control-plane block for THE SIGNAL-EMBEDDINGS
// PATH: whether the client collects feature vectors at all, and whether it
// sends them to Atlas. It rides the existing /v1/enrichment-settings poll — the
// client_telemetry precedent — so adopting it is a server change alone.
//
// Fields are pointers (like Remote's other blocks) so an absent key ("not set
// by the org") is distinct from an explicit false, and a field added by a newer
// daemon against an older Atlas degrades to the local value rather than zeroing
// the block.
//
// ⚠️ THE THIRD TOGGLE IS NOT HERE, and must not be added by analogy. `capture`
// — the extra ingest rows the vectors are computed from — is the SIDECAR'S
// (KELD_CAPTURE) and is fingerprinted into `parse_state`, so flipping it forces
// a reparse of every transcript that sees another append. These two are free
// either way, and that is exactly why they are separable from it: turning
// publishing off must not cost a full reparse to turn back on. An org key that
// could flip `capture` remotely would put a whole-corpus reparse behind a
// control-plane poll.
//
// BOTH DEFAULT OFF. Atlas has no consumer for a feature row yet, and this repo's
// standing rule is that rows nothing reads are opt-in and announced rather than
// quietly accumulated — the same reason KELD_TICK and KELD_BLOCKS ship off.
type Features struct {
	// Enabled governs collecting and storing the vectors locally.
	Enabled *bool `json:"enabled"`
	// Publish governs sending them to Atlas. Separate from Enabled because a
	// machine can usefully do the first without the second.
	Publish *bool `json:"publish"`
}

// FeaturesEnv and FeaturesPublishEnv are the local switches. Named here as well
// as in the features package so the precedence chain — env > agent-config.json
// > off, then the org override on top — is stated where the override lives.
const (
	FeaturesEnv        = "KELD_FEATURES"
	FeaturesPublishEnv = "KELD_FEATURES_PUBLISH"
)

// FeaturesLocalEnabled resolves the `features` toggle's LOCAL precedence only
// (KELD_FEATURES > agent-config.json > off) — the same computation NewLive
// folds into Live.base, exposed standalone for a caller that has no running
// daemon to ask (a diagnostic command reading settings.Load() directly).
//
// ⚠️ It cannot see an org's remote override: that only exists inside a running
// daemon's Live.eff, is never persisted, and there is no channel today that
// hands it to an out-of-process reader. A machine an org just switched on
// remotely reads as "off" here until the next restart. That is the safe
// direction to be wrong in for a diagnostic whose standing risk is nagging
// about a model nobody needs (see AGENTS.md) — undercounting costs a missed
// heads-up, overcounting costs a permanent false problem line.
func (s Settings) FeaturesLocalEnabled() bool { return featuresEnvBool(FeaturesEnv, s.Features) }
