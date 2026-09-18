package integrations

import (
	"encoding/json"
	"time"
)

// Why keld applied an adapter. A CLOSED set, published on the
// `integration.configured` client-event and on the row, because "keld wrote this
// tool's config" and "keld REPAIRED this tool's config" are different events and
// a fleet view that cannot tell them apart cannot count either.
const (
	// ReasonFirstSetup — the manifest did not record this tool. AC-3's case: a
	// tool appeared on the machine and auto-setup configured it.
	ReasonFirstSetup = "first_setup"
	// ReasonHookCommand — the config already recorded holds a keld hook command
	// that cannot execute as written (an upgrade preserves tool configs, so the
	// quoting fix reaches an old machine no other way).
	ReasonHookCommand = "hook_command"
	// ReasonTelemetryDrift — keld's own block holds a telemetry endpoint or
	// credential that is not the one this machine is paired with. The
	// 2026-09-18 incident; see telemetryDrifted.
	ReasonTelemetryDrift = "telemetry_drift"
)

// RepairNotes is the sentence each reason prints, written ONCE here for the
// reason Instructions is: the pane renders what the server said and maps
// nothing, so a second copy in JavaScript would be a second decision.
//
// ReasonFirstSetup has no sentence and is not a repair — a tool that was just
// configured for the first time already reads `restart_required` with the
// restart instruction beside it, and "Signal repaired this" would be untrue.
var RepairNotes = map[string]string{
	ReasonFirstSetup:     "",
	ReasonHookCommand:    "Signal repaired this tool's keld hook command, which an earlier version had written in a form the tool could not run. Restart it once to pick up the change.",
	ReasonTelemetryDrift: "Signal repaired this tool's telemetry settings, which held a credential this machine's daemon no longer accepts. Restart it once to pick up the change.",
}

// Repair is the record of keld rewriting its OWN block in one tool's config.
// Persisted, because the two halves are in different processes' worth of state:
// the detector repairs on a background poll inside the daemon, and
// GET /v1/integrations is computed from scratch on every request with no memory
// of it.
//
// Note is filled on the way OUT (computeOne) rather than stored, so a reworded
// sentence reaches every machine with the binary instead of only the ones that
// repair again afterwards.
type Repair struct {
	Reason string    `json:"reason"`
	At     time.Time `json:"at"`
	Note   string    `json:"note,omitempty"`
}

// repairsKey is this record's top-level key inside ~/.keld/state/integrations.json
// — the same document emitted_states and the lane facts live in, each under its
// own key and each read-modify-writing the whole thing.
const repairsKey = "repairs"

// LoadRepairs reads what keld has repaired, per source.
//
// An absent file, an unreadable one, or a document with no `repairs` key all
// answer EMPTY with no error: a machine that has never needed a repair is the
// normal case, and reporting a fault there would put one on every install.
func LoadRepairs() map[string]Repair {
	doc, err := readEmitDoc()
	if err != nil {
		return map[string]Repair{}
	}
	raw, ok := doc[repairsKey]
	if !ok {
		return map[string]Repair{}
	}
	out := map[string]Repair{}
	if err := json.Unmarshal(raw, &out); err != nil {
		// A corrupt key costs the pane one sentence, never the document.
		return map[string]Repair{}
	}
	return out
}

// RecordRepair remembers that keld repaired one tool's config. It REPLACES any
// previous entry for that source: the question the row asks is "what did keld
// last do to this file", and a history would have to be aged out by something,
// which is a policy nothing needs yet.
func RecordRepair(sourceID, reason string, at time.Time) error {
	all := LoadRepairs()
	all[sourceID] = Repair{Reason: reason, At: at.UTC()}
	buf, err := json.Marshal(all)
	if err != nil {
		return err
	}
	return putEmitDoc(repairsKey, buf)
}
