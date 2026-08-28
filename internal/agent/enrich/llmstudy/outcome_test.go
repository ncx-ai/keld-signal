package llmstudy

import "testing"

func TestOutcomesAlignWithWindowsAndReadForward(t *testing.T) {
	o := DefaultMineOpts()
	ws, err := Mine("testdata/session.jsonl", o)
	if err != nil {
		t.Fatal(err)
	}
	ocs, err := Outcomes("testdata/session.jsonl", o)
	if err != nil {
		t.Fatal(err)
	}
	if len(ocs) != len(ws) {
		t.Fatalf("outcomes=%d windows=%d; they must align 1:1", len(ocs), len(ws))
	}
	// Fixture turn 0 is followed by assistant prose, 2 tool calls, more prose,
	// then the second user turn.
	if ocs[0].AssistantTurns < 1 {
		t.Errorf("turn 0 AssistantTurns = %d, want >= 1", ocs[0].AssistantTurns)
	}
	if ocs[0].ToolCalls != 2 {
		t.Errorf("turn 0 ToolCalls = %d, want 2", ocs[0].ToolCalls)
	}
	if ocs[0].CodeLines != 3 {
		t.Errorf("turn 0 CodeLines = %d, want 3", ocs[0].CodeLines)
	}
	if ocs[0].Terminal {
		t.Error("turn 0 is not terminal: a later user turn exists")
	}
	// The last user turn has nothing after it.
	last := ocs[len(ocs)-1]
	if !last.Terminal {
		t.Error("final turn must be Terminal")
	}
	if last.AssistantTurns != 0 || last.ToolCalls != 0 {
		t.Errorf("final turn should have no forward work, got %+v", last)
	}
}

// Corrected is the label a router most wants to predict: the human's next message
// pushing back means the turn failed.
func TestOutcomeDetectsCorrectionInTheNextUserTurn(t *testing.T) {
	ocs, err := Outcomes("testdata/noise.jsonl", DefaultMineOpts())
	if err != nil {
		t.Fatal(err)
	}
	if len(ocs) == 0 {
		t.Fatal("no outcomes")
	}
	// noise.jsonl's user turns are: "refactor the publish retry path",
	// "keep going", "now ship it", "finally, tag the release" — none is a
	// correction, so no outcome should be flagged.
	for i, oc := range ocs {
		if oc.Corrected {
			t.Errorf("outcome %d flagged Corrected, but no user turn pushes back", i)
		}
	}
}

func TestOutcomeCorrectedTrueWhenNextTurnPushesBack(t *testing.T) {
	// Build the sequence directly to isolate the label logic.
	recs := []record{
		{role: RoleUser, text: "add retry", id: "a"},
		{role: RoleAssistant, text: "done"},
		{role: RoleUser, text: "no, that's wrong — revert it", id: "b"},
	}
	// Reuse the same forward-scan the exported path uses, via a tiny local mirror.
	oc := Outcome{Terminal: true}
	for j := 1; j < len(recs); j++ {
		if recs[j].role == RoleUser {
			oc.Corrected = hasCorrection(recs[j].text)
			oc.Terminal = false
			break
		}
		if recs[j].role == RoleAssistant {
			oc.AssistantTurns++
		}
	}
	if !oc.Corrected {
		t.Fatal("a pushback in the next user turn must set Corrected")
	}
}

func TestActionMarkerBaseline(t *testing.T) {
	yes := []string{"add retry to the poll", "fix the build", "do it", "go ahead", "commit and push"}
	no := []string{"why did that fail?", "what does this do", "thanks", "interesting"}
	for _, s := range yes {
		if !hasActionMarker(s) {
			t.Errorf("hasActionMarker(%q) = false, want true", s)
		}
	}
	for _, s := range no {
		if hasActionMarker(s) {
			t.Errorf("hasActionMarker(%q) = true, want false", s)
		}
	}
}
