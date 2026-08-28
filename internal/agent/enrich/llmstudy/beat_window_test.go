package llmstudy

import (
	"fmt"
	"strings"
	"testing"
)

func userTurn(s string) Turn      { return Turn{RoleUser, s} }
func assistantTurn(s string) Turn { return Turn{RoleAssistant, s} }

// TestBeatWindowsAreContiguous is the property the old geometry did not have at any K: every
// turn between two beats is read by one of them.
//
// Fails before the change by construction — there was no beat window at all; the beat was handed
// Mine's K=12 window, which on this corpus covers about a fifth of a five-prompt stride.
func TestBeatWindowsAreContiguous(t *testing.T) {
	var deltas []Window
	for i := 0; i < 10; i++ {
		deltas = append(deltas, Window{PromptID: "p", Turns: []Turn{
			userTurn("ask " + strings.Repeat("x", 20)),
			assistantTurn("answer " + strings.Repeat("y", 30)),
		}})
	}
	var bw BeatWindower
	first := bw.Next(deltas, 4)
	second := bw.Next(deltas, 9)
	if first.SpanTurns != 10 || second.SpanTurns != 10 {
		t.Fatalf("spans = %d and %d turns, want 10 each (5 prompts x 2 turns)",
			first.SpanTurns, second.SpanTurns)
	}
	if first.Dropped() != 0 || second.Dropped() != 0 {
		t.Errorf("turns were dropped on a span that fits: %d and %d", first.Dropped(), second.Dropped())
	}
	// The two spans partition the deltas: nothing read twice as NEW material, nothing skipped.
	if got := first.KeptTurns + second.KeptTurns; got != 20 {
		t.Errorf("the two windows cover %d of 20 turns", got)
	}
	// A third call with nothing new must not re-serve the previous stride.
	if again := bw.Next(deltas, 9); again.SpanTurns != 0 || again.Rendered != "" {
		t.Errorf("a beat asked for a stride with nothing new got %d turns back", again.SpanTurns)
	}
}

// TestBeatWindowsAreDisjoint pins stride equals window: no turn is read by two beats.
//
// It replaces the test that pinned the OPPOSITE property. That test asserted a carried overlap of
// ~28% of the previous window, and the overlap existed to give ChangedSubject shared ground —
// which measured 41 of 42 on refinements and then 0 of 46 on packets, because it was reading
// window adjacency rather than subject change. Nothing compares a beat to its predecessor now, so
// the re-read quarter of every window is spent instead on turns no window has ever carried.
func TestBeatWindowsAreDisjoint(t *testing.T) {
	// Distinct text per delta, so a turn appearing in two windows is unambiguous rather than a
	// coincidence of repeated boilerplate.
	var deltas []Window
	for i := 0; i < 20; i++ {
		deltas = append(deltas, Window{PromptID: "p", Turns: []Turn{
			userTurn(fmt.Sprintf("ask %d", i)),
			assistantTurn(fmt.Sprintf("answer %d %s", i, strings.Repeat("z", 200))),
		}})
	}
	var bw BeatWindower
	first := bw.Next(deltas, 9)
	second := bw.Next(deltas, 19)
	if first.Dropped() != 0 || second.Dropped() != 0 {
		t.Fatalf("the fixture must fit the bound to isolate the overlap question: dropped %d and %d",
			first.Dropped(), second.Dropped())
	}
	seen := map[string]bool{}
	for _, ln := range strings.Split(strings.TrimRight(first.Rendered, "\n"), "\n") {
		seen[ln] = true
	}
	for _, ln := range strings.Split(strings.TrimRight(second.Rendered, "\n"), "\n") {
		if seen[ln] {
			t.Errorf("the second beat re-reads a line of the first: %.60q", ln)
		}
	}
	// And the second window starts where the first stopped: disjoint AND contiguous, not
	// disjoint by skipping.
	if !strings.HasPrefix(second.Rendered, "user: ask 10\n") {
		t.Errorf("the second window does not begin at the turn after the first ended: %.40q",
			second.Rendered)
	}
}

// TestBeatWindowDropsWholeTurnsAndSaysSo covers the case the spec names: coverage exceeding the
// character bound. It must drop from the OLDEST end, at turn granularity, and mark the hole
// where it falls — a beat that silently skipped material is the defect the change exists to
// remove, and "silently" is the operative word.
func TestBeatWindowDropsWholeTurnsAndSaysSo(t *testing.T) {
	big := strings.Repeat("a sentence about the ledger. ", 40) // ~1,120 runes
	var deltas []Window
	for i := 0; i < 40; i++ {
		deltas = append(deltas, Window{PromptID: "p", Turns: []Turn{userTurn("ask"), assistantTurn(big)}})
	}
	var bw BeatWindower
	b := bw.Next(deltas, 39)
	if b.TotalRunes > BeatWindowChars {
		t.Fatalf("window is %d runes over the %d bound", b.TotalRunes, BeatWindowChars)
	}
	if b.Dropped() == 0 {
		t.Fatalf("a %d-turn span fitted inside %d runes; the fixture is not exercising the bound",
			b.SpanTurns, BeatWindowChars)
	}
	if !strings.Contains(b.Rendered, beatOmittedNotice) {
		t.Error("turns were dropped without the window saying so")
	}
	// Whole turns only: every rendered line is a line one of the turns produced.
	for _, ln := range strings.Split(b.Rendered, "\n") {
		if ln == "" || strings.HasPrefix(ln, "[turns since") {
			continue
		}
		if !strings.HasPrefix(ln, "user: ") && !strings.HasPrefix(ln, "assistant: ") &&
			!strings.HasPrefix(ln, "tool: ") {
			t.Errorf("a line is not a whole turn: %.60q", ln)
		}
	}
	// The newest turn survives — it is what the beat is answering about.
	if !strings.Contains(b.Rendered, big[:60]) {
		t.Error("the newest material was dropped; the bound must keep the tail")
	}
	// The notice sits where the hole is. With stride equal to window the dropped turns are the
	// head of this beat's own stride, so the marker leads — and the turns below it must all be
	// the ones that were kept, i.e. nothing is marked as missing that is actually present.
	if !strings.HasPrefix(b.Rendered, beatOmittedNotice) {
		t.Errorf("the hole is at the oldest end but the marker is not: %.60q", b.Rendered)
	}
}

// TestBeatWindowChargesItselfForTheHoleMarker pins the bound against the one thing in the window
// that is not a turn.
//
// The notice is written INTO the window, so the window's size includes it, but the fit was
// computed over turns alone — so a span that filled the bound exactly came out at
// BeatWindowChars + len(notice). Nothing caught it: the drop test's fixture leaves hundreds of
// runes of slack (whole-turn granularity rarely lands on the boundary), so the assertion it
// makes about TotalRunes passed for a reason unrelated to the arithmetic. That is the branch's
// recorded failure mode exactly — a test asserting a limit the implementation does not apply —
// so this fixture is sized to land ON the boundary: 16 turns of 1,000 rendered runes fit
// 16,000 exactly, and the 17th onward must be dropped.
func TestBeatWindowChargesItselfForTheHoleMarker(t *testing.T) {
	// renderedTurnLen = len("assistant") + 2 + len(text) + 1, so 988 runes of text costs 1,000.
	var deltas []Window
	for i := 0; i < 40; i++ {
		deltas = append(deltas, Window{PromptID: "p", Turns: []Turn{
			assistantTurn(strings.Repeat("ledger entry. ", 70) + strings.Repeat("x", 8)),
		}})
	}
	if got := renderedTurnLen(deltas[0].Turns[0]); got != 1000 {
		t.Fatalf("fixture turn costs %d runes, want exactly 1000 so the fit lands on the bound", got)
	}
	var bw BeatWindower
	b := bw.Next(deltas, 39)
	if b.Dropped() == 0 {
		t.Fatalf("a %d-turn span fitted; the fixture is not exercising the bound", b.SpanTurns)
	}
	if !strings.Contains(b.Rendered, beatOmittedNotice) {
		t.Fatal("turns were dropped without the window saying so")
	}
	if b.TotalRunes > BeatWindowChars {
		t.Errorf("the window is %d runes against the %d bound — %d over, and the overrun is the "+
			"hole marker, which the fit did not reserve for",
			b.TotalRunes, BeatWindowChars, b.TotalRunes-BeatWindowChars)
	}
	// And the number the coverage report is computed from must be the size of the string the
	// prompt actually carries, marker included, or the bound would be certified against a
	// quantity nothing sends.
	if b.TotalRunes != runeLen(b.Rendered) {
		t.Errorf("TotalRunes = %d but the rendered window is %d runes", b.TotalRunes, runeLen(b.Rendered))
	}
}

// TestBeatPromptHasABudgetNow closes the gap four fix rounds deferred: BeatPrompt asserted
// nothing about its arguments, on the grounds that trimToWindowCap bounded a mined window to
// 12,000 runes. Contiguous windows remove that accident — the stride is up to 52,148 runes
// before bounding — and an overflow here is silent, because the server truncates the prompt and
// the model answers about whatever survived.
func TestBeatPromptHasABudgetNow(t *testing.T) {
	// A real beat window plus a real record must be comfortably inside the budget.
	ok := BeatPrompt("counts: turns=300 user_turns=16\n", strings.Repeat("user: ask about it.\n", 300))
	if runeLen(ok) > BeatPromptCharBudget {
		t.Fatalf("a legitimate beat prompt is %d runes, over the %d budget", runeLen(ok), BeatPromptCharBudget)
	}
	defer func() {
		if recover() == nil {
			t.Error("an over-budget beat prompt did not trip the backstop")
		}
	}()
	BeatPrompt("counts: turns=1\n", strings.Repeat("user: x\n", BeatPromptCharBudget))
}

// TestBeatCoverageCountsWhatNoWindowRead is the accounting the design is judged on. It exists
// because the old geometry's shortfall was not merely bad, it was UNREPORTED: nothing anywhere
// said how much of a session's transcript no beat had ever seen.
func TestBeatCoverageCountsWhatNoWindowRead(t *testing.T) {
	var c BeatCoverage
	c.Add(BeatWindow{SpanTurns: 10, KeptTurns: 10, TotalRunes: 1000})
	c.Add(BeatWindow{SpanTurns: 10, KeptTurns: 5, TotalRunes: 1000})
	if got := c.TurnCoverage(); got != 75 {
		t.Errorf("coverage = %.1f%%, want 75", got)
	}
	// The count, not only the rate: 5 turns of this transcript were read by nothing.
	if got := c.UnreadTurns(); got != 5 {
		t.Errorf("turns read by no window = %d, want 5", got)
	}
	// A window that dropped turns carries the marker, so the two counts must agree — a hole
	// counted here but not marked in the window is the silent skip the design forbids.
	if c.Holed != 1 {
		t.Errorf("windows carrying a hole marker = %d, want 1", c.Holed)
	}
	if c.Windows != 2 || c.LargestRunes != 1000 {
		t.Errorf("windows=%d largest=%d", c.Windows, c.LargestRunes)
	}
	// An empty window (a beat asked for a stride with nothing new) must not count as a
	// window with zero coverage — that would report a cadence bug as a geometry failure.
	c.Add(BeatWindow{})
	if c.Windows != 2 {
		t.Errorf("an empty window was counted: windows=%d", c.Windows)
	}
}
