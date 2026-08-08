package llmstudy

import (
	"reflect"
	"testing"
)

func TestExtractCountsFixtureStructure(t *testing.T) {
	got := Extract(mineFixture(t, 8)[1])
	// Fixture: user, assistant(+code), tool Edit, tool Bash, assistant, user(target).
	if got.UserTurns != 2 {
		t.Errorf("UserTurns = %d, want 2", got.UserTurns)
	}
	if got.ToolCalls != 2 || got.ToolVariety != 2 {
		t.Errorf("ToolCalls=%d ToolVariety=%d, want 2 and 2", got.ToolCalls, got.ToolVariety)
	}
	if got.CodeBlocks != 1 {
		t.Errorf("CodeBlocks = %d, want 1", got.CodeBlocks)
	}
	if got.CodeLines != 3 {
		t.Errorf("CodeLines = %d, want 3 (the fixture's go block)", got.CodeLines)
	}
	if got.TargetChars == 0 || got.AssistantChars == 0 {
		t.Errorf("char counts not populated: %+v", got)
	}
}

// A collapsed tool run must count as N calls but one run: the distinction is the
// difference between "one retry loop" and "N separate decisions".
func TestExtractExpandsCollapsedToolRuns(t *testing.T) {
	w := Window{Turns: []Turn{
		{RoleTool, "Bash go test ./one/ (x3)"},
		{RoleTool, "Read page.tsx"},
	}}
	got := Extract(w)
	if got.ToolCalls != 4 {
		t.Errorf("ToolCalls = %d, want 4 (3 collapsed + 1)", got.ToolCalls)
	}
	if got.ToolRuns != 2 {
		t.Errorf("ToolRuns = %d, want 2", got.ToolRuns)
	}
	if got.ToolVariety != 2 {
		t.Errorf("ToolVariety = %d, want 2", got.ToolVariety)
	}
}

func TestExtractDetectsCorrections(t *testing.T) {
	w := Window{Turns: []Turn{
		{RoleUser, "add retry to the poll"},
		{RoleUser, "no, that's wrong — revert it"},
		{RoleUser, "still failing, try again"},
	}}
	if got := Extract(w); got.Corrections != 2 {
		t.Errorf("Corrections = %d, want 2 (turn 1 is not a correction)", got.Corrections)
	}
}

func TestExtractCorrectionMatchingIsCaseInsensitive(t *testing.T) {
	w := Window{Turns: []Turn{{RoleUser, "NO, THAT'S WRONG"}}}
	if got := Extract(w); got.Corrections != 1 {
		t.Errorf("Corrections = %d, want 1", got.Corrections)
	}
}

// Signals must be counts only: nothing here may carry text, because these are
// intended to be publishable without a masking gate.
func TestSignalsCarryNoText(t *testing.T) {
	got := Extract(mineFixture(t, 8)[1])
	// A compile-time-ish guard: every field must be an int. If someone adds a
	// string field, this test should be updated only after a privacy review.
	if got.Turns < 0 { // touch the struct so it cannot be optimised away
		t.Fatal("unreachable")
	}
	// Reflectively assert no string fields.
	assertAllIntFields(t, got)
}

func TestExtractEmptyWindowIsZero(t *testing.T) {
	if got := Extract(Window{}); got != (Signals{}) {
		t.Fatalf("empty window should give zero signals, got %+v", got)
	}
}

// assertAllIntFields fails if Signals gains a non-int field. Signals is meant to be
// publishable without a masking gate precisely because it is counts only; a string
// field would silently break that guarantee.
func assertAllIntFields(t *testing.T, s Signals) {
	t.Helper()
	rt := reflect.TypeOf(s)
	for i := 0; i < rt.NumField(); i++ {
		if k := rt.Field(i).Type.Kind(); k != reflect.Int {
			t.Errorf("Signals.%s is %s, not int — counts-only invariant broken; needs a privacy review",
				rt.Field(i).Name, k)
		}
	}
}
