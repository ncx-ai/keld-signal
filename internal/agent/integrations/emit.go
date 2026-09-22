package integrations

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// The four integration.* client-event codes. They are documented, in the same
// style as every other code, in docs/signal-client-events.md — and a test here
// pins that document to exactly four lines naming one, which is AC-5's own
// wording of the check.
const (
	// CodeBroken — a tool TRANSITIONED into broken. Exactly one per
	// transition, never one per poll: the whole point of persisting the last
	// emitted state is that a machine sitting broken for a week sends one
	// event, not one every minute.
	CodeBroken = "integration.broken"
	// CodeRecovered — a tool LEFT broken, whatever it left for.
	CodeRecovered = "integration.recovered"
	// CodeConfigured — the detector wrote keld's blocks into a tool's config.
	CodeConfigured = "integration.configured"
	// CodeReport — a person pressed "Report a problem" and a bundle was
	// written and queued.
	CodeReport = "integration.report"
)

// The severities these codes carry, as plain strings.
//
// ⚠️ This package does NOT import internal/agent/clientevents, and the
// direction is what forces that: the report bundle lives in clientevents and
// needs this package's ids and states, so an import here would be a cycle. The
// severity vocabulary is clientevents' and is mirrored as strings, with Sink
// as the seam — the daemon passes an adapter over *clientevents.Emitter.
const (
	SeverityInfo = "info"
	SeverityWarn = "warn"
)

// Sink is the narrow view of the client-events emitter this package needs.
// EmitExempt mirrors clientevents.Emitter.EmitExempt: it bypasses the org's
// min_severity floor while keeping the caller's severity.
type Sink interface {
	Emit(code, severity string, fields map[string]any)
	EmitExempt(code, severity string, fields map[string]any)
}

// Emission is one decided event: what to publish, at what severity, and
// whether it is exempt from the severity floor. Reconcile returns these rather
// than calling a Sink, so the decision is testable without an emitter and the
// caller keeps ownership of when the events actually go out.
type Emission struct {
	Code     string
	Severity string
	Fields   map[string]any
	// Exempt — pass through clientevents' EmitExempt rather than Emit.
	Exempt bool
}

// LaneCounts is how much each lane carried for one source inside the window.
//
// ⚠️ It is a COUNT, not an instant: the state rule (WS-C1's LaneFacts) asks
// "did anything arrive", and a person reading integration.broken asks "how
// much of what arrived" — 0 hook pointers against 41 telemetry batches is the
// sentence that names the fault, and two last-seen timestamps are not.
type LaneCounts struct {
	Hook    int
	Watcher int
	OTel    int
	Reader  int
}

// EmittedState is what this machine last told Atlas about one tool. Persisted
// under the `emitted_states` key of ~/.keld/state/integrations.json.
type EmittedState struct {
	State   State       `json:"state"`
	Surface SurfaceKind `json:"surface,omitempty"`
	// ToolVersion is recorded so a version that CHANGED while a tool stayed
	// broken is visible on the next comparison; it is never guessed (AC-7).
	ToolVersion string    `json:"tool_version,omitempty"`
	At          time.Time `json:"at"`
}

// Reconcile compares the freshly computed rows against the states this machine
// last emitted and returns the events to send, plus the state map to persist.
//
// The rule is a TRANSITION rule and nothing else:
//   - not broken → broken   ⇒ one CodeBroken
//   - broken → anything else ⇒ one CodeRecovered
//   - anything else          ⇒ nothing
//
// A first sighting is silent unless the tool is already broken. A machine that
// has never emitted holds no previous state, and announcing every row it finds
// would make a fresh install indistinguishable from a fleet-wide fault.
//
// counts may be nil or missing a source: a lane with no counter reports 0,
// which is the same thing the state rule concluded when it called the lane
// silent.
func Reconcile(now time.Time, current []Integration, prev map[string]EmittedState, counts map[string]LaneCounts, window time.Duration) ([]Emission, map[string]EmittedState) {
	next := make(map[string]EmittedState, len(current))
	var out []Emission
	windowH := window.Hours()

	for _, in := range current {
		was, known := prev[in.ID]
		next[in.ID] = EmittedState{
			State:       in.State,
			Surface:     in.BrokenLane,
			ToolVersion: in.ToolVersion,
			At:          now.UTC(),
		}

		switch {
		case in.State == Broken && (!known || was.State != Broken):
			c, counted := counts[in.ID]
			out = append(out, brokenEmission(in, c, counted, windowH))
		case in.State != Broken && known && was.State == Broken:
			c, counted := counts[in.ID]
			out = append(out, recoveredEmission(in, c, counted, windowH))
		}
	}

	// A source that vanished from the catalogue (or from this daemon's view)
	// keeps its last state rather than being dropped: forgetting a broken tool
	// would re-announce it the moment it came back into view.
	for id, st := range prev {
		if _, ok := next[id]; !ok {
			next[id] = st
		}
	}
	return out, next
}

// brokenEmission builds the CodeBroken event. Every field AC-5 names is
// present, and tool_version is present even when empty — "" is the honest
// answer for a version we could not read, and an absent key would be
// indistinguishable from a daemon too old to send one.
func brokenEmission(in Integration, c LaneCounts, counted bool, windowH float64) Emission {
	return Emission{
		Code:     CodeBroken,
		Severity: SeverityWarn,
		Fields:   laneFields(in, c, counted, windowH, nil),
	}
}

// recoveredEmission builds the CodeRecovered event.
//
// ⚠️ It rides EmitExempt, for the reason service.recovered does: under the
// default `warn` floor an info recovery is dropped while the warn-level break
// that preceded it was delivered, so every tool that ever broke would look
// permanently broken in a fleet view.
func recoveredEmission(in Integration, c LaneCounts, counted bool, windowH float64) Emission {
	return Emission{
		Code:     CodeRecovered,
		Severity: SeverityInfo,
		Exempt:   true,
		Fields:   laneFields(in, c, counted, windowH, map[string]any{"state": string(in.State)}),
	}
}

// laneFields is the shared field set: who, which lane, what version, over what
// window, and how much each lane carried.
// ⚠️ `counted` DECIDES WHETHER THE FOUR COUNTERS APPEAR AT ALL, and until it
// existed they were always published as 0 on a real daemon. `detector.go` calls
// Reconcile with a nil `counts` map, nothing anywhere produces a LaneCounts
// outside this package's own tests, and `counts[id]` on a nil map yields the
// zero value — so every integration.broken this client has ever sent said
// hook_n=0, watcher_n=0, otel_n=0, reader_n=0.
//
// Four zeros are not a missing field, they are a MEASUREMENT: "nothing arrived
// on any lane" — the most incriminating sentence this event can carry, from a
// count nobody took. That is the confident-negative-from-a-check-nobody-ran
// failure this repo forbids by name (facets_degraded, thin vs absent,
// "Absent means NOT RECORDED, never zero").
//
// So an absent count is now an ABSENT KEY, and a measured 0 still publishes —
// "counted, none arrived" is exactly the fact the event exists to carry, and
// collapsing it into the same shape as "nobody counted" is what caused this.
// The keys return the moment a producer is wired; this does not remove the
// field from the contract, it stops the field lying while no producer exists.
func laneFields(in Integration, c LaneCounts, counted bool, windowH float64, extra map[string]any) map[string]any {
	f := map[string]any{
		"source":       in.ID,
		"surface":      string(in.BrokenLane),
		"tool_version": in.ToolVersion,
		"window_h":     windowH,
	}
	if counted {
		f["hook_n"] = c.Hook
		f["watcher_n"] = c.Watcher
		f["otel_n"] = c.OTel
		f["reader_n"] = c.Reader
	}
	for k, v := range extra {
		f[k] = v
	}
	return f
}

// ConfiguredEmission is AC-3's event: the detector configured a tool that
// appeared after the daemon started.
//
// ⚠️ backupPath is TAKEN AND NOT PUBLISHED. It is an absolute path, which the
// redaction gate would blank anyway; what a fleet view can act on is whether a
// backup was written at all. The path is shown on the pane, from the route,
// where it never crosses the machine.
func ConfiguredEmission(source, adapter, backupPath string, restartRequired, auto bool) Emission {
	return Emission{
		Code:     CodeConfigured,
		Severity: SeverityInfo,
		Exempt:   true,
		Fields: map[string]any{
			"source":           source,
			"adapter":          adapter,
			"backup":           backupPath != "",
			"restart_required": restartRequired,
			"auto":             auto,
		},
	}
}

// ReportEmission is AC-6's event. fields is the bundle's summary, already
// reduced to primitives by the caller (internal/agent/clientevents.Bundle's
// EventFields) — this function only stamps the code and severity, so all four
// codes are decided in one file.
func ReportEmission(source string, fields map[string]any) Emission {
	f := map[string]any{"source": source}
	for k, v := range fields {
		f[k] = v
	}
	return Emission{Code: CodeReport, Severity: SeverityWarn, Fields: f}
}

// Emit hands a batch of decided emissions to the sink, honouring each one's
// floor exemption.
func Emit(s Sink, ems []Emission) {
	if s == nil {
		return
	}
	for _, e := range ems {
		if e.Exempt {
			s.EmitExempt(e.Code, e.Severity, e.Fields)
			continue
		}
		s.Emit(e.Code, e.Severity, e.Fields)
	}
}

// emitStatePath is ~/.keld/state/integrations.json — the file WS-C1's lane
// facts also live in. Named for this file's own key rather than for the file,
// deliberately: the path helper belongs to whichever of the two lands first,
// and a duplicate exported StatePath in one package would not compile.
func emitStatePath() string {
	return filepath.Join(paths.StateDir(), "integrations.json")
}

// emittedStatesKey is this file's top-level key inside that document.
const emittedStatesKey = "emitted_states"

// LoadEmittedStates reads the last emitted state per source.
//
// An absent file, an unreadable one, or a document with no emitted_states key
// all answer EMPTY with no error: a machine that has never emitted is the
// normal first run, and treating it as a fault would make every fresh install
// report one.
func LoadEmittedStates() (map[string]EmittedState, error) {
	doc, err := readEmitDoc()
	if err != nil {
		return map[string]EmittedState{}, err
	}
	raw, ok := doc[emittedStatesKey]
	if !ok {
		return map[string]EmittedState{}, nil
	}
	out := map[string]EmittedState{}
	if err := json.Unmarshal(raw, &out); err != nil {
		// A corrupt key is re-derived on the next compute; the cost is one
		// duplicate event, never a lost document.
		return map[string]EmittedState{}, nil
	}
	return out, nil
}

// SaveEmittedStates writes the emitted states back.
//
// ⚠️ IT IS A READ-MODIFY-WRITE OF THE WHOLE DOCUMENT, NEVER A TRUNCATE. WS-C1
// writes its lane facts into the same file under its own key; a marshal of
// just this map would delete them silently, and the lane facts are what the
// state rule reads — so the next compute would conclude every lane was silent
// and this file would emit a broken event for every tool on the machine.
func SaveEmittedStates(states map[string]EmittedState) error {
	buf, err := json.Marshal(states)
	if err != nil {
		return err
	}
	return putEmitDoc(emittedStatesKey, buf)
}

// putEmitDoc sets ONE key of ~/.keld/state/integrations.json and rewrites the
// document, keys it knows nothing about included. Every writer of that file goes
// through here for the reason SaveEmittedStates gives above: `repairs` and the
// lane facts share it, and a marshal of one map would delete the others.
func putEmitDoc(key string, value json.RawMessage) error {
	doc, err := readEmitDoc()
	if err != nil {
		return err
	}
	if doc == nil {
		doc = map[string]json.RawMessage{}
	}
	doc[key] = value

	out, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	path := emitStatePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}

// readEmitDoc reads ~/.keld/state/integrations.json as an opaque object, so
// keys this file knows nothing about survive a write byte for byte.
func readEmitDoc() (map[string]json.RawMessage, error) {
	raw, err := os.ReadFile(emitStatePath())
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]json.RawMessage{}, nil
		}
		return map[string]json.RawMessage{}, err
	}
	doc := map[string]json.RawMessage{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		// Not an object: start a fresh one rather than refuse forever. The
		// only thing that could be lost is a document nothing here wrote.
		return map[string]json.RawMessage{}, nil
	}
	return doc, nil
}
