package publish

import (
	"encoding/json"
	"strings"
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
		got, id, _ := SubagentRunOf(c.path)
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

// TestParentSessionIsRecoveredFromThePath pins the recovery that makes a
// delegated run attributable to the session that launched it.
//
// ⚠️ WITHOUT THIS A SUBAGENT BLOCK NAMES NOBODY: its own session_id is
// `agent-<hash>`, which no tool_event carries and no other row shares. The
// composite that matters — a session's blocks plus the blocks of the runs it
// launched — is then uncomputable, and it is the ONLY honest total for a session
// that delegated, because a parent block's mix excludes its subagents' calls
// while its cost includes them.
//
// The layout is measured, not assumed: 445 of 445 subagent transcripts on the
// frozen corpus sit at `<parent-session-uuid>/subagents/agent-<hash>.jsonl`, and
// on all 444 readable ones the path-derived parent equals the `sessionId` inside
// the file, with zero mismatches.
func TestParentSessionIsRecoveredFromThePath(t *testing.T) {
	const parent = "129e9a80-12a1-478f-9d47-cd68c47b8739"
	real := "/h/.claude/projects/-h-proj/" + parent + "/subagents/agent-ac2ba6f8d8.jsonl"

	isRun, id, got := SubagentRunOf(real)
	if !isRun || id != "ac2ba6f8d8" || got != parent {
		t.Fatalf("SubagentRunOf(real) = (%v, %q, %q), want (true, %q, %q)",
			isRun, id, got, "ac2ba6f8d8", parent)
	}

	// ⚠️ A LAYOUT THAT DOES NOT NAME A PARENT MUST YIELD "", NEVER A GUESS. A
	// wrong parent silently reassigns one person's delegated work to another
	// session, which no consumer can detect and no test downstream would catch.
	for _, p := range []string{
		"/h/projects/-h-proj/notauuid/subagents/agent-x.jsonl", // holder is not a session
		"/h/projects/-h-proj/subagents/agent-x.jsonl",          // no session level at all
		"/h/projects/-h-proj/" + parent + "/agent-x.jsonl",     // not under subagents/
		"agent-x.jsonl", // bare filename
	} {
		isRun, _, got := SubagentRunOf(p)
		if !isRun {
			t.Errorf("SubagentRunOf(%q): isRun = false; the FILENAME still says it is a run", p)
		}
		if got != "" {
			t.Errorf("SubagentRunOf(%q) parent = %q, want \"\" — a parent is never guessed", p, got)
		}
	}

	// A main-line transcript is not a run and has no parent.
	if isRun, id, got := SubagentRunOf("/h/projects/-h-proj/" + parent + ".jsonl"); isRun || id != "" || got != "" {
		t.Errorf("main-line transcript read as a run: (%v, %q, %q)", isRun, id, got)
	}
}

// TestParentSessionRidesTheWire keeps the field from being declared and never
// populated — the exact defect that left activity_classes on no row at all.
func TestParentSessionRidesTheWire(t *testing.T) {
	const parent = "129e9a80-12a1-478f-9d47-cd68c47b8739"
	path := "/h/.claude/projects/-h-proj/" + parent + "/subagents/agent-zz.jsonl"
	b := BuildBlock(enrich.BlockCharacterisation{SessionID: "agent-zz"}, "actor", time.Now(), path)
	if b.ParentSessionID != parent {
		t.Fatalf("ParentSessionID = %q, want %q", b.ParentSessionID, parent)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"parent_session_id":"`+parent+`"`) {
		t.Errorf("parent_session_id absent from the wire: %s", raw)
	}
	// A main-line block stays byte-identical to the payload before this field.
	main := BuildBlock(enrich.BlockCharacterisation{SessionID: parent}, "actor", time.Now(),
		"/h/.claude/projects/-h-proj/"+parent+".jsonl")
	if mraw, _ := json.Marshal(main); strings.Contains(string(mraw), "parent_session_id") {
		t.Errorf("parent_session_id present on a main-line block: %s", mraw)
	}
}
