package llmstudy

import (
	"strings"
	"testing"
)

// TestEventCheckIsShapeOnly documents where the line is: this check reads only the answer, so it
// cannot judge whether an entry is true — that is the anchoring guard's fact test and the blind
// reviewers' judgement, in that order. What it can enforce is that each entry is a whole
// one-line statement of usable length and that the list does not repeat itself.
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
	if _, err := checkBeatEvents([]string{"the ledger was reconciled\nand then reopened"}); err == nil {
		t.Error("a multi-line entry was accepted; one entry is one bullet")
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

// The per-entry cap is set against BeatCap, not chosen: a subject and three entries at the cap
// must still fit the stored beat, or an ordinary answer would be trimmed by fitBeatEvents.
func TestEventCapLetsThreeEntriesFitTheBeatCap(t *testing.T) {
	subject := strings.Repeat("s", beatSubjectMaxRunes)
	entry := strings.Repeat("e", beatEventMaxRunes)
	if n := runeLen(renderBeat(subject, []string{entry, entry, entry}, nil, nil)); n > BeatCap {
		t.Errorf("a subject and three max-length entries render to %d runes, over BeatCap %d",
			n, BeatCap)
	}
}
