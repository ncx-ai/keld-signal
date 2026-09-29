package publish

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

func TestSubagentRunOf(t *testing.T) {
	for _, c := range []struct {
		path string
		want bool
		id   string
	}{
		{"/u/.claude/projects/-p/agent-a0a7163f629f2e65a.jsonl", true, "a0a7163f629f2e65a"},
		{"agent-abc.jsonl", true, "abc"},
		// A session transcript, which is the whole point of the distinction.
		{"/u/.claude/projects/-p/d6638eab-dc62-483d-b9c7-88e96fcf63d6.jsonl", false, ""},
		// Shapes that must NOT be read as a run: a bare prefix, a directory named
		// like one, and an agent-shaped name that is not a transcript.
		{"agent-.jsonl", false, ""},
		{"/u/agent-x/session.jsonl", false, ""},
		{"agent-abc.json", false, ""},
		{"", false, ""},
	} {
		got, id := SubagentRunOf(c.path)
		if got != c.want || id != c.id {
			t.Errorf("SubagentRunOf(%q) = (%v, %q), want (%v, %q)", c.path, got, id, c.want, c.id)
		}
	}
}

// A main-line block must be BYTE-IDENTICAL to the payload before these fields
// existed. That is the contract `projects` and `concepts` already keep, and it
// is what lets an old Atlas and a new client be indistinguishable on the wire.
func TestMainLineBlockCarriesNeitherField(t *testing.T) {
	row := BuildBlock(enrich.BlockCharacterisation{SessionID: "s1"}, "actor",
		time.Unix(0, 0), "/u/projects/-p/d6638eab.jsonl")
	b, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"is_subagent_run", "subagent_id"} {
		if _, ok := m[k]; ok {
			t.Errorf("main-line block published %q; it must be omitted entirely", k)
		}
	}
}

func TestSubagentBlockCarriesBothFields(t *testing.T) {
	row := BuildBlock(enrich.BlockCharacterisation{SessionID: "s1"}, "actor",
		time.Unix(0, 0), "/u/projects/-p/agent-a5c682161621ef6f4.jsonl")
	if !row.IsSubagentRun {
		t.Error("a block cut from agent-*.jsonl must be marked as a subagent run")
	}
	if row.SubagentID != "a5c682161621ef6f4" {
		t.Errorf("SubagentID = %q, want the id from the filename", row.SubagentID)
	}
}

// ⚠️ THE FAILURE THIS FIELD'S REQUIRED PARAMETER EXISTS TO PREVENT.
//
// The attribution pass REPUBLISHES a block, rebuilding the row from scratch. If
// the transcript path were optional there, a republish would strip the marker
// off a row that had carried it -- invisible on both sides, since the row is
// upserted over its identity key and the old value is simply overwritten.
//
// This asserts the marker SURVIVES the republish shape: build, then rebuild the
// way attrib.republish does, and compare.
func TestMarkerSurvivesAttributionRepublish(t *testing.T) {
	const path = "/u/projects/-p/agent-a5c682161621ef6f4.jsonl"
	b := enrich.BlockCharacterisation{SessionID: "s1"}
	first := BuildBlock(b, "actor", time.Unix(0, 0), path)
	// what attrib.republish does: BuildBlock again from the same characterisation,
	// then layer attribution on top.
	again := WithProjects(BuildBlock(b, "actor", time.Unix(0, 0), path), nil, "", nil, nil)
	if !again.IsSubagentRun || again.SubagentID != first.SubagentID {
		t.Fatalf("republish lost the subagent marker: got (%v, %q), want (%v, %q)",
			again.IsSubagentRun, again.SubagentID, first.IsSubagentRun, first.SubagentID)
	}
}
