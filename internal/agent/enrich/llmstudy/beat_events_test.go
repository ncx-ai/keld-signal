package llmstudy

import (
	"strings"
	"testing"
)

// TestEventPromptModelsTheEmptyAnswerWithoutNamingAProhibition is the diagnosis, as a test.
//
// The empty answer has to LOOK like a normal output, so it appears inside the worked examples
// rather than as a permission. And the prompt must not name the behaviour it is trying to avoid:
// when the stock-opener rule was reworded to name forbidden phrasings, those openings went from
// 2 to 4 — a prompt summons what it names.
func TestEventPromptModelsTheEmptyAnswerWithoutNamingAProhibition(t *testing.T) {
	p := BeatEventPrompt("user: reconcile the March ledger\n")
	if !strings.Contains(p, "nothing was completed in this stretch") {
		t.Error("the empty answer is not modelled as one of the example answers")
	}
	if !strings.Contains(p, "each of these is a normal answer") {
		t.Error("the examples are not presented as normal answers")
	}
	for _, forbidden := range []string{"do not", "never", "forbidden", "must not", "avoid"} {
		if strings.Contains(strings.ToLower(p), forbidden) {
			t.Errorf("the event prompt names a prohibition (%q); prompts summon what they name",
				forbidden)
		}
	}
	if !strings.Contains(p, "what happened") || !strings.Contains(p, "past tense") {
		t.Error("the event prompt does not ask what happened")
	}
	// The fused question is the thing being replaced; asking it here would reinstate it.
	if strings.Contains(p, "where it has got to") || strings.Contains(p, "how far") {
		t.Error("the event prompt asks for a state rather than for events")
	}
}

// TestEventCheckIsShapeOnly documents where the line is: this pass reads the evidence, so nothing
// above it can judge whether an event is true, and the checks are the ones that can be made.
func TestEventCheckIsShapeOnly(t *testing.T) {
	if _, err := checkBeatEvents([]string{"too short"}); err == nil {
		t.Error("an entry under the floor was accepted")
	}
	if _, err := checkBeatEvents([]string{strings.Repeat("x", beatEventMaxRunes+1)}); err == nil {
		t.Error("an entry over the cap was accepted")
	}
	if _, err := checkBeatEvents([]string{"", "  "}); err == nil {
		t.Error("an all-blank list was accepted")
	}
	got, err := checkBeatEvents([]string{"the ledger was reconciled", "The Ledger Was Reconciled",
		"the export was rerun and failed"})
	if err != nil {
		t.Fatalf("checkBeatEvents: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("duplicate event kept: %v", got)
	}
}
