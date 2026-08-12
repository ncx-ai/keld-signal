package llmstudy

import (
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

// TestBeatWindowsShareGroundWithTheirPredecessor pins the stride overlap. Without it,
// consecutive beat windows are disjoint and ChangedSubject is asked whether the subject changed
// while being shown material that shares nothing with the previous beat's — everything looks new.
func TestBeatWindowsShareGroundWithTheirPredecessor(t *testing.T) {
	line := func(i int) Turn { return assistantTurn("turn " + strings.Repeat("z", 200)) }
	var deltas []Window
	for i := 0; i < 20; i++ {
		deltas = append(deltas, Window{PromptID: "p", Turns: []Turn{userTurn("ask"), line(i)}})
	}
	var bw BeatWindower
	first := bw.Next(deltas, 9)
	second := bw.Next(deltas, 19)
	if first.OverlapTurns != 0 {
		t.Errorf("the first beat has no predecessor but reports %d overlap turns", first.OverlapTurns)
	}
	if second.OverlapTurns == 0 {
		t.Fatal("the second beat shares no ground with the first — consecutive windows are disjoint")
	}
	// The overlap must be the previous span's TAIL, i.e. text the first beat actually read.
	head := strings.SplitN(second.Rendered, "\n", 2)[0]
	if !strings.Contains(first.Rendered, head) {
		t.Errorf("the overlap is not material the previous beat read: %.60q", head)
	}
	// The dial is a share of the PREVIOUS window, which is the quantity the spec states.
	ofPrev := 100 * float64(second.OverlapRunes) / float64(second.PrevSpanRunes)
	if ofPrev < 20 || ofPrev > beatOverlapPct+1 {
		t.Errorf("overlap is %.1f%% of the previous window, want ~%d%%", ofPrev, beatOverlapPct)
	}
	// Which for two spans of similar size is a smaller share of the NEW window. Both are
	// reported by the sweep; neither is the other.
	ofWindow := 100 * float64(second.OverlapRunes) / float64(second.TotalRunes)
	if ofWindow < 10 || ofWindow > 30 {
		t.Errorf("overlap is %.1f%% of the new window, which is neither incidental nor dominant", ofWindow)
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
	// The notice sits between the overlap and the kept turns, where the hole actually is,
	// rather than at the top where it would read as "the session started earlier".
	if strings.HasPrefix(b.Rendered, beatOmittedNotice) && b.OverlapTurns > 0 {
		t.Error("the hole marker is at the top of a window that has overlap above it")
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
	c.Add(BeatWindow{SpanTurns: 10, KeptTurns: 5, OverlapRunes: 280, PrevSpanRunes: 1000, TotalRunes: 1000})
	if got := c.TurnCoverage(); got != 75 {
		t.Errorf("coverage = %.1f%%, want 75", got)
	}
	if got := c.OverlapPct(); got != 14 {
		t.Errorf("overlap = %.1f%%, want 14", got)
	}
	if got := c.OverlapOfPrevPct(); got != 28 {
		t.Errorf("overlap of previous = %.1f%%, want 28", got)
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
