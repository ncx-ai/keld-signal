package llmstudy

import (
	"fmt"
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

// promptWindow recovers the conversation window from an ASSEMBLED prompt by landmark, for
// tests that have only the finished string.
//
// Deliberately NOT what the backstop does: assertPromptWithinBudget is handed fitTurns'
// own return value, because landmark recovery is defeated by content that quotes a
// landmark (task-7b fix round 4 — a quoted windowHeader silences the floor check, a quoted
// tail marker fails a healthy prompt). A test that builds its own input knows its content
// contains no such quote, so landmark recovery is sound HERE and is the only option: the
// window is not otherwise observable from outside. See
// TestQuotedMarkersCannotDefeatTheBackstop for the two shapes this helper would get wrong,
// and note that they are exactly why the production check does not use it.
func promptWindow(t *testing.T, p, marker, tail string) string {
	t.Helper()
	start := strings.Index(p, marker)
	if start < 0 {
		t.Fatalf("assembled prompt is missing its window marker %q", marker)
	}
	start += len(marker)
	end := strings.Index(p[start:], tail)
	if end < 0 {
		t.Fatalf("assembled prompt is missing its tail marker %q", tail)
	}
	return p[start : start+end]
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

// TestFloorIsReachableAtRealisticInputScale is the capacity decision's regression test —
// the reason DefaultPromptCharBudget went to 14,000 at ctx 8192.
//
// The round-2 review's finding was NOT that some pathological input breaches MinTurnChars;
// the round-3 bounds handle those. It was that the floor is unreachable at input scales a
// real session produces — a record holding path-shaped Subjects, a path-heavy recency
// anchor, a large measured-facts block on the create path — with every bound in this
// package honoured. Confirmed by setting DefaultPromptCharBudget back to 11,000 and running
// this exact test: BOTH subtests fail, refine with a window of 517 runes against the 1,600
// floor and create with 1,147 (the review's independently measured figure for the create
// path was 1,141). A panic is the RIGHT response to a starved window, but it aborts the
// measurement sweep instead of costing one digest, so the input scale had to be admitted
// rather than the failure tolerated. Restored to 14,000: refine window 1,675, create 1,847.
//
// Nothing here is an adversarial construction: no hand-built list exceeds its own
// accumulation-time cap, prev is what CapSections returns, the beats are a full ladder and
// the view is over its cap (both discretionary, both expected to yield). The two
// deliberately UNBOUNDED dimensions used — SessionRecord.Projects' per-entry length and
// DigestFacts' Topics/Entities counts — are named as such where they are built.
func TestFloorIsReachableAtRealisticInputScale(t *testing.T) {
	t.Run("refine, record at realistic scale", func(t *testing.T) {
		defer failOnBackstop(t)
		p := DigestUpdatePromptFrom(realisticPrev(), realisticRefineInput())
		assertFitsAndKeepsTheFloor(t, p, windowHeader, updateSectionsMarker)
	})

	t.Run("create, large measured-facts block", func(t *testing.T) {
		defer failOnBackstop(t)
		facts := realisticFactsBlock()
		t.Logf("facts block: %d runes", len([]rune(facts)))
		p := DigestCreatePromptWithView("work session",
			strings.Repeat("user: a filler turn about the work\n", 400),
			strings.Repeat("user: an early turn about the work\n", 400), facts)
		assertFitsAndKeepsTheFloor(t, p, createWindowHeader, createSectionsMarker)
	})
}

// failOnBackstop turns the backstop's panic into a test failure naming it, so a capacity
// regression reads as "the floor is unreachable again" rather than as a bare panic
// stack in the middle of a sweep.
func failOnBackstop(t *testing.T) {
	t.Helper()
	if r := recover(); r != nil {
		t.Fatalf("the backstop fired on a realistic input scale — the budget no longer "+
			"admits it: %v", r)
	}
}

func assertFitsAndKeepsTheFloor(t *testing.T, p, marker, tail string) {
	t.Helper()
	total := len([]rune(p))
	window := len([]rune(promptWindow(t, p, marker, tail)))
	t.Logf("total %d runes (budget %d, margin %d), window %d (floor %d, margin %d)",
		total, DefaultPromptCharBudget, DefaultPromptCharBudget-total,
		window, MinTurnChars, window-MinTurnChars)
	if total > DefaultPromptCharBudget {
		t.Errorf("prompt %d runes exceeds the %d-rune budget", total, DefaultPromptCharBudget)
	}
	if window < MinTurnChars {
		t.Errorf("window starved to %d runes, below the %d-rune floor", window, MinTurnChars)
	}
}

// realisticPrev is a steady-state prior report: every prose section at its own cap and a
// full list of insights/unresolved at DefaultListEntryCap — i.e. exactly what CapSections
// returns, with identifier-dense content so the retain-list is at its bound too.
func realisticPrev() Digest {
	var id int
	prev := Digest{
		Synopsis:  packIdentifiers(&id, DefaultSynopsisCap),
		Done:      packIdentifiers(&id, DefaultProseCap),
		Happened:  packIdentifiers(&id, DefaultHappenedCap),
		Structure: packIdentifiers(&id, DefaultStructureCap),
		Current:   packIdentifiers(&id, DefaultProseCap),
		Why:       packIdentifiers(&id, DefaultProseCap),
		Next:      packIdentifiers(&id, DefaultProseCap),
	}
	for i := 0; i < DefaultListCap; i++ {
		prev.Insights = append(prev.Insights, packIdentifiers(&id, DefaultListEntryCap))
		prev.Unresolved = append(prev.Unresolved, packIdentifiers(&id, DefaultListEntryCap))
	}
	return prev
}

// realisticRefineInput is one refinement of a long engineering session: a full record
// (subjects and projects path-shaped, tools, focus, turning points at their cap), a full
// beat ladder, an over-cap session view, and a focus shift whose newest turn names files.
func realisticRefineInput() RefineInput {
	rec := SessionRecord{Turns: 900, UserTurns: 300, ToolCalls: 2100, Corrections: 11}
	// Subjects at maxSubjectTermLen, the bound Observe now enforces per term, and at
	// MaxRecordSubjects entries — the largest record Observe can actually build.
	subjects := make([]string, MaxRecordSubjects)
	for i := range subjects {
		subjects[i] = pathOfLen(maxSubjectTermLen, i)
	}
	rec.Subjects = subjects
	// Projects' PER-ENTRY LENGTH IS UNENFORCED (only MaxRecordProjects bounds the count),
	// so this length is a realistic figure and not derived from a constant: a checkout
	// path in a nested monorepo. Named here rather than quietly used, because "a test
	// certified a bound the code does not enforce" is the recurring defect on this branch.
	const realisticProjectPathLen = 56
	projects := make([]string, MaxRecordProjects)
	for i := range projects {
		projects[i] = pathOfLen(realisticProjectPathLen, 100+i)
	}
	rec.Projects = projects
	rec.Tools = []ToolCount{{"Read", 900}, {"Edit", 800}, {"Bash", 700}, {"Write", 600}, {"Grep", 500}, {"Glob", 400}}
	rec = rec.WithFocus("engineering", "software-development", 0.87)
	for i := 1; i <= MaxRecordTurningPoints; i++ {
		rec = rec.NoteTurningPoint(i*7, TriggerFocusShift)
	}

	beats := make([]Beat, MaxBeatSelection)
	for i := range beats {
		beats[i] = Beat{Ordinal: i + 1, Text: strings.Repeat("w", BeatCap), ChangedSubject: i%2 == 0}
	}

	// The newest user turn names maxRecentSubjects path-shaped files, so the focus-shift
	// anchor fires at its own bound rather than being skipped.
	var last strings.Builder
	last.WriteString("user: now look at")
	for i := 0; i < maxRecentSubjects; i++ {
		last.WriteString(" " + pathOfLen(maxSubjectTermLen, 200+i))
	}
	last.WriteString("\n")

	return RefineInput{
		// At sessionLabelCap: the label is fixed overhead ahead of everything else.
		SessionLabel: strings.Repeat("a real session label about the work underway ", 5),
		Record:       rec,
		Beats:        beats,
		SessionView:  strings.Repeat("user: an early turn about the work\n", 400), // > SessionViewCap
		NewTurns:     strings.Repeat("user: a filler turn about the work\n", 400) + last.String(),
		Why:          TriggerFocusShift,
	}
}

// pathOfLen builds a distinct, path-shaped token of exactly n runes, so a test can size a
// subject or project against a constant instead of a literal string.
func pathOfLen(n, seed int) string {
	head := fmt.Sprintf("internal/agent/enrich/llmstudy/p%03d_", seed)
	if len(head) >= n {
		return head[:n]
	}
	return head + strings.Repeat("x", n-len(head)-3) + ".go"
}

// realisticFactsBlock is the create path's measured-context block, rendered by DigestFacts
// itself rather than hand-assembled.
//
// DigestFacts.Topics and Entities have NO enforced count bound (an earlier note in this
// package called the facts block "naturally bounded", which is true of the counts and the
// tool profile but not of these two lists), so the counts below are realistic figures for a
// long session, not derived ones — flagged for the same reason as the project path length.
// Sized to the ~6,000-rune block the capacity decision was taken against.
//
// RESIDUAL, named rather than hidden: because the block is caller-supplied, uncapped and
// pure fixed overhead on the create path (which has no beat ladder to yield, only the
// view), a big enough block starves the window at ANY budget. Measured on this
// construction at the 14,000 budget: ~6,000 runes leaves the floor intact with room to
// spare, ~8,800 does not (window 1,357). Bounding DigestFacts' two unbounded lists is the
// fix; it is not in task-7b's scope, and the backstop makes the failure loud rather than
// silent in the meantime.
func realisticFactsBlock() string {
	var topics, entities []string
	for i := 0; i < 40; i++ {
		topics = append(topics, pathOfLen(maxSubjectTermLen, 300+i))
	}
	for i := 0; i < 40; i++ {
		entities = append(entities, "vendor: "+pathOfLen(maxSubjectTermLen, 400+i))
	}
	return DigestFacts{
		Turns: 900, UserTurns: 300, ToolCalls: 2100, ToolVariety: 9, Corrections: 11, CorrectedTurns: 7,
		Tools: []ToolCount{{"Read", 900}, {"Edit", 800}, {"Bash", 700}, {"Write", 600}, {"Grep", 500}, {"Glob", 400}},
	}.WithPlace("keld-signal", "feat/llm-classify-study", "keld").
		WithFocus("engineering", "software-development", 0.87).
		WithEnrichment(topics, entities).Block()
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
