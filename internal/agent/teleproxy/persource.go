package teleproxy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// UnknownSource is where a forward is recorded when the payload does not name a
// tool this client knows.
//
// ⚠️ IT IS A STATED ANSWER, NOT A FALLBACK TO THE LIKELIEST TOOL. The state
// rule reads "did THIS tool's telemetry arrive"; crediting an unrecognised
// payload to whichever tool happens to be configured would turn a broken lane
// into a working one, which is the confident-wrong-answer failure the whole
// integrations surface exists to remove. A machine whose forwards all land here
// reads as "no telemetry for any tool", which is at least true.
const UnknownSource = "otel:unknown"

// serviceNameKey is the OTLP resource attribute every tool measured here sets.
//
// It is the RESOURCE's attribute, not a record's: the resource describes the
// process that produced the batch, so one batch has exactly one, and a payload
// mixing two tools is not a shape any of them produces.
const serviceNameKey = "service.name"

// serviceNameToSource maps what the tools ACTUALLY send to the catalogue id the
// rest of the daemon keys on (integrations.Catalogue, the spool pointer's
// source, the watcher root's SourceID).
//
// ⚠️ MEASURED ON 2026-09-15 FROM REAL PAYLOADS, not read off documentation:
// each fixture under testdata/ was captured by pointing the tool's own OTLP
// exporter at a loopback listener and running one prompt.
//
//	Claude Code 2.1.271   service.name = "claude-code"     (+ service.version)
//	Codex 0.153.4         service.name = "codex_exec"      (`codex exec`)
//	Gemini CLI            service.name = "gemini-cli"      (+ session.id)
//
// Note the two spellings — a hyphen for the two JS/TS tools, an underscore for
// the Rust one — which is why this is a table rather than a normalisation rule.
//
// COWORK IS DELIBERATELY ABSENT and will never appear here: its sandbox blocks
// egress, so nothing of its OTLP reaches this proxy at all. Its telemetry is
// mirrored host-side by internal/agent/promptlog, which posts to Atlas directly
// and not through this path; its catalogue entry expects the WATCHER lane only.
var serviceNameToSource = map[string]string{
	"claude-code": "claude_code",
	"gemini-cli":  "gemini_cli",
}

// codexPrefix is the one prefix rule, and it has a reason a table would not
// serve: Codex names the resource after its ENTRYPOINT, so `codex exec` sends
// codex_exec and the TUI and MCP server send their own. They are one
// integration and one config file, so they share one source id. A future
// entrypoint we have not seen lands in the same place rather than in the
// unknown bucket, which is the right default for a tool we do know.
const codexPrefix = "codex"

// ServiceName returns the OTLP resource's service.name, or "" when the payload
// carries none. Exported so a test can pin the literal the tools send against
// the mapping that consumes it — a mapping and a fixture that drift apart would
// otherwise both pass.
func ServiceName(body []byte) string {
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return ""
	}
	return findServiceName(v)
}

func findServiceName(v any) string {
	switch t := v.(type) {
	case map[string]any:
		if k, ok := t["key"].(string); ok && k == serviceNameKey {
			if val, ok := t["value"].(map[string]any); ok {
				if s, ok := val["stringValue"].(string); ok && s != "" {
					return s
				}
			}
		}
		for _, child := range t {
			if s := findServiceName(child); s != "" {
				return s
			}
		}
	case []any:
		for _, child := range t {
			if s := findServiceName(child); s != "" {
				return s
			}
		}
	}
	return ""
}

// SourceOf names which tool an OTLP payload came from, as a catalogue source
// id, or UnknownSource.
//
// ⚠️ A SERVICE NAME IS NOT TEXT, and that is why reading one here is allowed at
// all: it is a fixed identifier the tool sets on its own process, in the same
// class as the session ids SessionIDs already reads — never a body, never
// prose, never an offset.
func SourceOf(body []byte) string {
	name := strings.ToLower(strings.TrimSpace(ServiceName(body)))
	if name == "" {
		return UnknownSource
	}
	if id, ok := serviceNameToSource[name]; ok {
		return id
	}
	if name == codexPrefix || strings.HasPrefix(name, codexPrefix+"_") || strings.HasPrefix(name, codexPrefix+"-") {
		return "codex"
	}
	return UnknownSource
}

// SourcesPath is where the per-source record lives: BESIDE telemetry.json, not
// inside it.
//
// ⚠️ A separate file, deliberately. telemetry.json is written by noteForward on
// a throttle and read by two of doctor's checks; a second writer with its own
// cadence inside the same document is exactly how one of them silently drops
// the other's keys — the failure integrations.SaveEmittedStates has to
// read-modify-write around one directory over.
func SourcesPath() string { return filepath.Join(paths.StateDir(), "telemetry-sources.json") }

// sourceState is the on-disk shape of the per-source record.
type sourceState struct {
	// Sources maps a catalogue source id (plus UnknownSource) to when telemetry
	// for it last REACHED Atlas. Never when it was attempted: an attempt would
	// make a machine with no network read as working, which is the same
	// distinction LastForward already draws.
	Sources map[string]time.Time `json:"sources"`
}

// SourcesOnDisk reads the per-source record. Empty, never an error, when
// nothing has forwarded — the refusal SessionsOnDisk makes: "not tracked yet"
// must never be reported as "nothing is arriving".
func SourcesOnDisk() map[string]time.Time {
	data, err := os.ReadFile(SourcesPath())
	if err != nil {
		return map[string]time.Time{}
	}
	var s sourceState
	if json.Unmarshal(data, &s) != nil || s.Sources == nil {
		return map[string]time.Time{}
	}
	return s.Sources
}

// LastForwardForSource answers row 6 of the decision table for one tool: when
// did THIS tool's telemetry last reach Atlas. ok is false when this machine has
// never recorded a forward for it, which is not the same as zero.
func LastForwardForSource(id string) (time.Time, bool) {
	at, ok := SourcesOnDisk()[id]
	return at, ok
}

// sourceRecord is the in-memory half, loaded at proxy construction.
type sourceRecord struct {
	mu      sync.Mutex
	sources map[string]time.Time
	path    string
	// lastPersisted throttles the write exactly as noteForward does: the state
	// rule asks for a coarse instant, not a write per OTLP batch.
	lastPersisted time.Time
}

// newSourceRecord loads whatever is on disk.
//
// ⚠️ LOAD, don't start empty — the same trap the session record documents. A
// daemon restart that began with an empty map would write that map back on its
// first forward and erase every other tool's history, so a tool that had gone
// quiet an hour ago would look like a tool that had never forwarded.
func newSourceRecord() *sourceRecord {
	return &sourceRecord{sources: SourcesOnDisk(), path: SourcesPath()}
}

// note records that telemetry for source reached Atlas at now.
//
// A source seen for the FIRST time forces the write regardless of the throttle,
// for noteForward's reason: throttling a first sighting loses the one fact the
// per-source check needs, since a tool that emitted once and stopped would be
// indistinguishable from one that never emitted at all.
func (r *sourceRecord) note(now time.Time, source string) {
	if r == nil || source == "" {
		return
	}
	r.mu.Lock()
	if r.sources == nil {
		r.sources = map[string]time.Time{}
	}
	_, seen := r.sources[source]
	r.sources[source] = now
	due := !seen || now.Sub(r.lastPersisted) >= persistEvery
	if due {
		r.lastPersisted = now
	}
	snapshot := make(map[string]time.Time, len(r.sources))
	for k, v := range r.sources {
		snapshot[k] = v.UTC()
	}
	path := r.path
	r.mu.Unlock()

	if !due || path == "" {
		return
	}
	buf, err := json.Marshal(sourceState{Sources: snapshot})
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return
	}
	// Best effort, like noteForward: a failed write costs a stale diagnostic,
	// never telemetry. The map is bounded by the source vocabulary plus
	// UnknownSource, so unlike the session record it needs no eviction.
	_ = os.WriteFile(path, buf, 0o600)
}
