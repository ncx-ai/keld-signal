package watch

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexFixtureDir holds the captured Codex rollouts. They are REAL files taken
// from ~/.codex/sessions and redacted by scripts/redact-rollout.py — never
// hand-written. See testdata/codex/PROVENANCE.md for which rollout each came
// from and at which cli_version.
const codexFixtureDir = "testdata/codex"

// codexFixtureVersions maps each fixture to the cli_version its session_meta
// must report. A fixture whose head says something else is not the file the
// provenance claims it is.
var codexFixtureVersions = map[string]string{
	"rollout-0.125.jsonl":   "0.125.0",
	"rollout-0.151.jsonl":   "0.151.0",
	"rollout-0.153.4.jsonl": "0.153.4",
}

// codexFixtureLines reads a fixture as decoded JSONL. Every line must decode:
// a fixture that does not is a fixture somebody edited by hand.
func codexFixtureLines(t *testing.T, name string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Join(codexFixtureDir, name))
	if err != nil {
		t.Fatalf("open fixture %s: %v", name, err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	var out []map[string]any
	for n := 1; sc.Scan(); n++ {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("%s line %d does not decode: %v", name, n, err)
		}
		out = append(out, m)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan %s: %v", name, err)
	}
	return out
}

func codexPayload(line map[string]any) map[string]any {
	p, _ := line["payload"].(map[string]any)
	return p
}

func codexPayloadType(line map[string]any) string {
	s, _ := codexPayload(line)["type"].(string)
	return s
}

// codexIsUserMessageEvent reports the classic human-turn shape:
// event_msg whose payload.type is user_message.
func codexIsUserMessageEvent(line map[string]any) bool {
	t, _ := line["type"].(string)
	return t == "event_msg" && codexPayloadType(line) == "user_message"
}

// codexIsUserMessageItem reports the item-model human-turn shape:
// event_msg / item_completed whose item.type is UserMessage.
func codexIsUserMessageItem(line map[string]any) bool {
	t, _ := line["type"].(string)
	if t != "event_msg" || codexPayloadType(line) != "item_completed" {
		return false
	}
	item, _ := codexPayload(line)["item"].(map[string]any)
	it, _ := item["type"].(string)
	return it == "UserMessage"
}

// TestCodexFixturesMatchProduction is TR-AC-4: the Codex fixtures are captured
// files, not hand-written ones, and no `user_message` line in any of them
// carries an `ordinal` — the field the old identity scheme keyed on and which
// no rollout ever supplied for a human turn.
//
// ⚠️ The stricter reading "no fixture LINE carries an ordinal" cannot hold
// against production and measuring it is how we learned so: Codex 0.148–0.151
// stamps a per-line `ordinal` on EVERY record, its `item_completed`
// `UserMessage` items included. So the 0.151 fixture carries them, and
// stripping them would make the fixture stop resembling the file it was
// captured from — the exact defect TR-AC-4 exists to prevent. The assertion is
// therefore AC-4 verbatim (no ordinal on a `user_message` line, which holds on
// all three) plus TestCodex0151KeepsItsProductionOrdinals below, so nobody
// "tidies" them away. Nothing in watch/ or resolve/ may read `ordinal` either
// way; that is pinned separately by TestCodexIdentityNeverComesFromOrdinal.
func TestCodexFixturesMatchProduction(t *testing.T) {
	for name, wantVersion := range codexFixtureVersions {
		lines := codexFixtureLines(t, name)
		if len(lines) == 0 {
			t.Fatalf("%s is empty", name)
		}

		// The head must be a real session_meta at the claimed cli_version.
		head := lines[0]
		if ht, _ := head["type"].(string); ht != "session_meta" {
			t.Fatalf("%s line 1 is %q, want session_meta", name, ht)
		}
		meta := codexPayload(head)
		if got, _ := meta["cli_version"].(string); got != wantVersion {
			t.Fatalf("%s cli_version=%q, want %q", name, got, wantVersion)
		}
		if id, _ := meta["id"].(string); id == "" {
			t.Fatalf("%s session_meta has no id", name)
		}

		// AC-4: no `user_message` line carries an ordinal.
		humanTurns := 0
		for i, ln := range lines {
			isEvent := codexIsUserMessageEvent(ln)
			if !isEvent && !codexIsUserMessageItem(ln) {
				continue
			}
			humanTurns++
			if _, ok := ln["ordinal"]; ok && isEvent {
				t.Errorf("%s line %d is a user_message carrying an ordinal; identity must come from turn_context", name, i+1)
			}
		}
		if humanTurns == 0 {
			t.Errorf("%s carries no human turn at all", name)
		}

		// Every human turn must be preceded by a turn_context carrying a
		// turn_id — the identity the watcher keys on.
		if !codexHasTurnContextWithID(lines) {
			t.Errorf("%s carries no turn_context with a turn_id", name)
		}
	}
}

func codexHasTurnContextWithID(lines []map[string]any) bool {
	for _, ln := range lines {
		if t, _ := ln["type"].(string); t != "turn_context" {
			continue
		}
		if id, _ := codexPayload(ln)["turn_id"].(string); id != "" {
			return true
		}
	}
	return false
}

// TestCodex0151WritesOnlyTheItemShape pins the reason the 0.151 fixture exists:
// that release writes the human turn ONLY as an item_completed UserMessage item
// and emits zero event_msg/user_message lines. A watcher that reads just the
// classic shape captures nothing at all on those machines.
func TestCodex0151WritesOnlyTheItemShape(t *testing.T) {
	lines := codexFixtureLines(t, "rollout-0.151.jsonl")
	var classic, items int
	for _, ln := range lines {
		if codexIsUserMessageEvent(ln) {
			classic++
		}
		if codexIsUserMessageItem(ln) {
			items++
		}
	}
	if classic != 0 {
		t.Errorf("0.151 fixture has %d event_msg/user_message lines, want 0", classic)
	}
	if items < 1 {
		t.Errorf("0.151 fixture has %d item_completed UserMessage items, want >= 1", items)
	}
}

// TestCodexFixtureProvenanceIsRecorded keeps the fixtures traceable: a captured
// file with no record of where it came from is indistinguishable from an
// invented one a release later.
func TestCodexFixtureProvenanceIsRecorded(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(codexFixtureDir, "PROVENANCE.md"))
	if err != nil {
		t.Fatalf("read PROVENANCE.md: %v", err)
	}
	for name := range codexFixtureVersions {
		if !strings.Contains(string(b), name) {
			t.Errorf("PROVENANCE.md does not name %s", name)
		}
	}
}

// TestCodex0151KeepsItsProductionOrdinals is the other half of AC-4, and it
// asserts the presence of the very field the scheme was wrongly built on.
// Codex 0.148-0.151 stamps an `ordinal` on every line; the fixture must keep
// them, because a fixture quietly cleaned of an awkward field is how an
// identity scheme that matched no real file shipped green in the first place.
func TestCodex0151KeepsItsProductionOrdinals(t *testing.T) {
	lines := codexFixtureLines(t, "rollout-0.151.jsonl")
	withOrdinal := 0
	for _, ln := range lines {
		if _, ok := ln["ordinal"]; ok {
			withOrdinal++
		}
	}
	if withOrdinal != len(lines) {
		t.Fatalf("0.151 fixture: %d of %d lines carry an ordinal, want all of them (do not strip production fields)", withOrdinal, len(lines))
	}
	// And the versions that bracket it carry none at all, which is why an
	// ordinal could never have been the identity.
	for _, name := range []string{"rollout-0.125.jsonl", "rollout-0.153.4.jsonl"} {
		for i, ln := range codexFixtureLines(t, name) {
			if _, ok := ln["ordinal"]; ok {
				t.Errorf("%s line %d carries an ordinal; that release writes none", name, i+1)
			}
		}
	}
}
