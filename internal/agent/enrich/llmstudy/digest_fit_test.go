package llmstudy

import (
	"strings"
	"testing"
)

// The whole point: no prompt may exceed the budget, however large the window.
func TestPromptsStayInsideTheBudget(t *testing.T) {
	huge := strings.Repeat("user: do a thing\nassistant: did the thing\n", 4000)
	facts := "counts: turns=900 corrections=3\n"
	if got := len([]rune(DigestCreatePrompt("work session", huge, facts))); got > DefaultPromptCharBudget {
		t.Errorf("create prompt %d runes exceeds budget %d", got, DefaultPromptCharBudget)
	}
	prev := Digest{
		Done: strings.Repeat("a", DefaultProseCap), Happened: strings.Repeat("b", DefaultProseCap),
		Structure: strings.Repeat("c", DefaultStructureCap), Current: strings.Repeat("d", DefaultProseCap),
		Why: strings.Repeat("e", DefaultProseCap), Next: strings.Repeat("f", DefaultProseCap),
		Insights: []string{"one", "two"}, Unresolved: []string{"three"},
	}
	if got := len([]rune(DigestUpdatePrompt(prev, "work session", huge, facts))); got > DefaultPromptCharBudget {
		t.Errorf("update prompt %d runes exceeds budget %d", got, DefaultPromptCharBudget)
	}
}

// Clipping keeps the newest turns: they are what current/next/unresolved describe, and
// the oldest are already folded into the prior digest's cumulative sections.
func TestClippingKeepsTheNewestTurns(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 3000; i++ {
		b.WriteString("user: filler turn\n")
	}
	b.WriteString("user: THE NEWEST TURN\n")
	p := DigestCreatePrompt("work session", b.String(), "counts: turns=1\n")
	if !strings.Contains(p, "THE NEWEST TURN") {
		t.Error("clipping dropped the newest turn")
	}
	if !strings.Contains(p, "earlier turns omitted") {
		t.Error("clipping must say it happened, not silently truncate")
	}
}

// A window that already fits must pass through untouched — no spurious notice.
func TestSmallWindowIsNotClipped(t *testing.T) {
	p := DigestCreatePrompt("work session", "user: hello\n", "counts: turns=1\n")
	if strings.Contains(p, "earlier turns omitted") {
		t.Error("a small window must not be clipped")
	}
}

// windowOf strips fitTurns' own notice prefix so callers measure the actual turn content
// (what current/why/next/unresolved are written from), not the notice that precedes it.
func windowOf(clipped string) string {
	return strings.TrimPrefix(clipped, omittedNotice)
}

// TestFitTurnsLineBoundaryTrimCannotBreachTheFloor is the fix for task-7b finding (b):
// after slicing to `room` runes, fitTurns used to trim back to the next '\n' UNCONDITIONALLY
// so the window would not open mid-word. That discards up to one whole line beyond what
// `room` already computed, so the window could land below MinTurnChars even though `room`
// itself cleared it. Both shapes from the brief are exercised here — many short lines
// (a small, compounding overshoot) and one long pasted turn straddling the cut (a
// near-total collapse, because the first '\n' the trim finds is that turn's own terminator,
// deep into the clipped window).
//
// overhead is chosen so the room LEFT FOR CONTENT after fitTurns' own internal reservation
// for omittedNotice (len 97) lands at exactly MinTurnChars — no slack at all, the tightest
// boundary at which the bug can bite. (Using DefaultPromptCharBudget-MinTurnChars directly
// as the overhead would test a confound instead: the notice reservation alone already
// eats 97 runes out of that room regardless of trim behaviour, so the pre-trim content
// would already sit below the floor and the test could not isolate the trim bug from that
// separate, expected reservation.) Any slack beyond this exact boundary would let the naive
// trim's loss be absorbed without the floor actually being breached, and a test with that
// much slack survives the bug instead of catching it (the same lesson
// TestWindowKeepsItsFloorAtTheBoundary documents for fitDiscretionary).
//
// Confirmed by reverting the `room < MinTurnChars || ... >= MinTurnChars` guard back to an
// unconditional trim and re-running:
//   - many short lines: window measured 1575 runes, 25 below the 1600 floor. Test fails.
//   - one long line straddling the cut: window measured 11 runes, 1589 below the floor.
//     Test fails.
//
// With the guard restored both report the full `room` (1600), because in both cases
// trimming to the boundary would have breached the floor, so fitTurns falls back to the
// untrimmed (mid-line) window instead — a mid-line open is a smaller loss than starving the
// four sections the window is the only evidence for.
// TestPromptBudgetViolationCatchesEachInvariant exercises promptBudgetViolation
// directly, on hand-built strings — independent of how a violating prompt might arise —
// so the backstop's own logic (task-7b fix round 3, finding A) is proven correct in
// isolation before anything else relies on it firing.
func TestPromptBudgetViolationCatchesEachInvariant(t *testing.T) {
	const marker = "\nWINDOW:\n"
	const tail = "\nTAIL\n"

	t.Run("over budget", func(t *testing.T) {
		p := strings.Repeat("x", DefaultPromptCharBudget+1) + marker + strings.Repeat("y", 2000) + tail
		if err := promptBudgetViolation(p, marker, tail); err == nil {
			t.Error("an over-budget prompt was not flagged")
		}
	})

	t.Run("clipped window below the floor", func(t *testing.T) {
		p := "head" + marker + omittedNotice + "short" + tail
		if err := promptBudgetViolation(p, marker, tail); err == nil {
			t.Error("a clipped window below MinTurnChars was not flagged")
		}
	})

	t.Run("short window WITHOUT clipping is not flagged", func(t *testing.T) {
		// The exact case TestSmallWindowIsNotClipped guards in the real assembly: a
		// window under MinTurnChars that was never clipped (no omittedNotice prefix) is
		// a genuinely short conversation, not starvation, and must not be flagged.
		p := "head" + marker + "hi" + tail
		if err := promptBudgetViolation(p, marker, tail); err != nil {
			t.Errorf("an unclipped short window was flagged: %v", err)
		}
	})

	t.Run("within budget, clipped window at the floor is not flagged", func(t *testing.T) {
		p := "head" + marker + omittedNotice + strings.Repeat("z", MinTurnChars) + tail
		if err := promptBudgetViolation(p, marker, tail); err != nil {
			t.Errorf("a healthy prompt was flagged: %v", err)
		}
	})
}

// TestBackstopCatchesAnUnboundedInputNoOtherFixTouches is the fix for task-7b finding
// (A): four rounds have each fixed a NAMED leak, and every review since has found the
// round after had missed one (the retain-list, open-item count, TurningPoints,
// SessionLabel, the omitted-turns notice). This backstop exists so the pattern does not
// need a fifth instance found by hand — so this test deliberately does NOT use any of
// the leaks findings (B)-(G) already named and fixed. It uses SessionRecord.Subjects,
// constructed directly rather than through Observe (which caps it at
// MaxRecordSubjects): SessionRecord.Block() joins ALL of Subjects with no cap of its
// own, exactly the same "accumulation-time cap, no render-time backstop" shape
// MaxRecordTurningPoints has for TurningPoints — proving the backstop protects a
// dimension none of this round's specific fixes touch, not just the ones that were
// found and named.
func TestBackstopCatchesAnUnboundedInputNoOtherFixTouches(t *testing.T) {
	subjects := make([]string, 2000)
	for i := range subjects {
		subjects[i] = strings.Repeat("s", 20)
	}
	rec := SessionRecord{Subjects: subjects, Turns: 1, hasFocus: false}
	in := RefineInput{SessionLabel: "work session", Record: rec, NewTurns: "user: hi\n"}

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected the backstop to panic on an unbounded SessionRecord.Subjects, it did not")
		} else {
			t.Logf("backstop fired as expected: %v", r)
		}
	}()
	DigestUpdatePromptFrom(Digest{}, in)
}

// TestWellFormedInputsDoNotTripTheBackstop is the negative case: the backstop must not
// fire on ordinary, healthy prompts, or it becomes noise no one can trust. Every other
// passing test in this package that calls DigestUpdatePromptFrom/DigestCreatePromptWithView
// already demonstrates this implicitly (a panic would fail them too); this test names
// the property explicitly.
func TestWellFormedInputsDoNotTripTheBackstop(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("backstop fired on an ordinary, healthy prompt: %v", r)
		}
	}()
	_ = DigestCreatePrompt("work session", "user: hello, this is a short conversation\n", "counts: turns=1\n")
	_ = DigestUpdatePrompt(Digest{Done: "x", Insights: []string{"a"}, Unresolved: []string{"b"}},
		"work session", "user: hello again\n", "counts: turns=2\n")
}

func TestFitTurnsLineBoundaryTrimCannotBreachTheFloor(t *testing.T) {
	overhead := DefaultPromptCharBudget - (MinTurnChars + len([]rune(omittedNotice)))

	t.Run("many short lines", func(t *testing.T) {
		const lineLen = 35 // matches the brief's own measurement of this shape
		var b strings.Builder
		for i := 0; i < 500; i++ {
			b.WriteString(strings.Repeat("a", lineLen-1))
			b.WriteString("\n")
		}
		got := windowOf(fitTurns(b.String(), overhead))
		n := len([]rune(got))
		t.Logf("window: %d runes (floor %d, margin %d)", n, MinTurnChars, n-MinTurnChars)
		if n < MinTurnChars {
			t.Errorf("window %d runes breached the floor of %d", n, MinTurnChars)
		}
	})

	t.Run("one long line straddling the cut", func(t *testing.T) {
		huge := strings.Repeat("x", 5000)
		turns := "user: filler\n" + huge + "\nuser: tail\n"
		got := windowOf(fitTurns(turns, overhead))
		n := len([]rune(got))
		t.Logf("window: %d runes (floor %d, margin %d)", n, MinTurnChars, n-MinTurnChars)
		if n < MinTurnChars {
			t.Errorf("window %d runes breached the floor of %d", n, MinTurnChars)
		}
	})
}
