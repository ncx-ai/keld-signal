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

// The per-entry cap is MEASURED, and the measurement is a pilot beat that was lost to the
// previous value: all five ladder attempts rejected over one 179-rune entry. A bound that
// discards the answer is the defect this whole design exists to remove, so the entry cap now sits
// above the longest entry the pilot produced and the TOTAL is bounded where bounding costs only a
// marked drop (fitBeatEvents).
func TestEventCapAdmitsTheLongestPilotEntry(t *testing.T) {
	const measured = "The Sub-CA selection for Apple Developer certificates was discussed and " +
		"confirmed to be G2 (Xcode 11.4.1 or later) due to its longer validity and " +
		"compatibility with modern tooling"
	if runeLen(measured) > beatEventMaxRunes {
		t.Errorf("the entry that cost a beat is still over the cap: %d runes against %d",
			runeLen(measured), beatEventMaxRunes)
	}
	if _, err := checkBeatEvents([]string{measured}); err != nil {
		t.Errorf("the entry that cost a beat is still rejected: %v", err)
	}
}
