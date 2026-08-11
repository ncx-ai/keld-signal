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
	t.Run("over budget", func(t *testing.T) {
		p := strings.Repeat("x", DefaultPromptCharBudget+1)
		if err := promptBudgetViolation(p, strings.Repeat("y", 2000)); err == nil {
			t.Error("an over-budget prompt was not flagged")
		}
	})

	t.Run("clipped window below the floor", func(t *testing.T) {
		window := omittedNotice + "short"
		if err := promptBudgetViolation("head"+window+"tail", window); err == nil {
			t.Error("a clipped window below MinTurnChars was not flagged")
		}
	})

	t.Run("short window WITHOUT clipping is not flagged", func(t *testing.T) {
		// The exact case TestSmallWindowIsNotClipped guards in the real assembly: a
		// window under MinTurnChars that was never clipped (no omittedNotice prefix) is
		// a genuinely short conversation, not starvation, and must not be flagged.
		if err := promptBudgetViolation("head hi tail", "hi"); err != nil {
			t.Errorf("an unclipped short window was flagged: %v", err)
		}
	})

	t.Run("within budget, clipped window at the floor is not flagged", func(t *testing.T) {
		window := omittedNotice + strings.Repeat("z", MinTurnChars)
		if err := promptBudgetViolation("head"+window+"tail", window); err != nil {
			t.Errorf("a healthy prompt was flagged: %v", err)
		}
	})

	t.Run("a window quoting a marker is measured as itself", func(t *testing.T) {
		// The whole point of taking the window as an argument: its CONTENT is arbitrary
		// conversation text and may contain either literal the assembly writes around it.
		// Nothing about the check may depend on that.
		window := omittedNotice + strings.Repeat("z", MinTurnChars) +
			windowHeader + updateSectionsMarker + createWindowHeader + createSectionsMarker
		if err := promptBudgetViolation("head"+window+"tail", window); err != nil {
			t.Errorf("a healthy window that quotes the assembly's own markers was flagged: %v", err)
		}
	})
}

// TestTailLenMeasuresWhatTheAssemblyAppends pins both tail-length functions to the thing
// they claim to measure: the runes each assembly writes AFTER the window.
//
// Positional, not by search — the last createTailLen()/updateTailLen() runes of the finished
// prompt must begin exactly at the sections marker. That catches both defects fix round 4
// found in updateTailLen (finding 5) with one assertion: a byte count is larger than the
// rune count on these em-dash-bearing blocks, so the slice starts too early and lands inside
// the conversation window; and a re-typed copy of the marker literal that drifts from
// updateSectionsMarker changes the length without changing what the assembly writes.
//
// Both were live: updateTailLen counted bytes AND re-hardcoded "\nProduce the UPDATED
// report, same sections:\n" after fix round 3 introduced the constant precisely to stop that
// (its commit message claimed the fix; the code did not have it). createTailLen was already
// correct, which is why it is pinned here too — the correct one is as easy to regress as the
// wrong one was to miss.
//
// Confirmed by restoring the byte-counting, literal-duplicating version and re-running:
// the refine subtest fails, reporting 5,697 (bytes) where the tail is 5,673 runes — the
// slice starts 24 runes early, inside the window ("ON (evidence):\nuser: hi\n\nProduce the
// UPDATED report..."). 5,697 is also the "instructional tail" figure earlier rounds of this
// task quoted as runes; it was bytes.
func TestTailLenMeasuresWhatTheAssemblyAppends(t *testing.T) {
	cases := []struct {
		name   string
		p      string
		n      int
		marker string
	}{
		{
			name:   "refine",
			p:      DigestUpdatePrompt(Digest{Done: "x"}, "work session", "user: hi\n", "counts: turns=2\n"),
			n:      updateTailLen(),
			marker: updateSectionsMarker,
		},
		{
			name:   "create",
			p:      DigestCreatePrompt("work session", "user: hi\n", "counts: turns=2\n"),
			n:      createTailLen(),
			marker: createSectionsMarker,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := []rune(c.p)
			if c.n > len(r) {
				t.Fatalf("tail length %d exceeds the whole prompt (%d runes)", c.n, len(r))
			}
			tail := string(r[len(r)-c.n:])
			if !strings.HasPrefix(tail, c.marker) {
				t.Errorf("the last %d runes do not begin at the sections marker — the tail "+
					"length measures something other than what the assembly appends; got %.70q",
					c.n, tail)
			}
		})
	}
}

// TestQuotedMarkersCannotDefeatTheBackstop is the fix for task-7b fix round 4 (findings 1
// and 2): the backstop used to re-derive the window by strings.Index-ing the literal
// headings around it, so CONTENT quoting a heading moved the measured span. Both directions
// are exercised, because they fail in opposite ways and one fix closes both — the assembly
// now hands the check fitTurns' own return value.
//
// Both shapes are reachable without any adversary. prev.Unresolved is model output and this
// model demonstrably echoes prompt headings back (that is why LeakedPromptWords exists);
// the conversation side is self-inflicted, since this harness mines transcripts of its own
// development, in which both literals appear.
//
// Confirmed by restoring the landmark version of the check (locating the window by
// strings.Index on the heading pair) and re-running: BOTH subtests fail. The quoted-heading
// case reports no panic at all — its landmark span starts inside the "OPEN ITEMS" block, so
// it does not begin with omittedNotice and the floor check is skipped entirely, while the
// REAL window is 800 runes against the 1,600 floor with the total inside budget: silent, the
// worst shape. The quoted-tail case panics on a healthy prompt, "clipped to 135 runes",
// against a real window in the thousands.
func TestQuotedMarkersCannotDefeatTheBackstop(t *testing.T) {
	t.Run("an open item quoting windowHeader cannot silence the floor check", func(t *testing.T) {
		in := starvingRefineInput()
		prev := realisticPrev()
		// The newest open item, so tailN keeps it, and short enough to survive
		// promptOpenItemCap intact. It lands in the "OPEN ITEMS FROM THAT REPORT" block,
		// which the assembly writes BEFORE the window — so the first Index hit for
		// windowHeader is here, not at the real window.
		prev.Unresolved = append(prev.Unresolved, windowHeader+"quoted back at us")

		defer func() {
			r := recover()
			if r == nil {
				t.Fatal("a starved window went unreported because an open item quoted " +
					"windowHeader — the backstop measured the wrong span")
			}
			msg := fmt.Sprint(r)
			t.Logf("backstop fired as expected: %v", msg)
			// It must fire on the FLOOR, not merely on the total: a budget violation here
			// would mean the test proved nothing about the window at all.
			if !strings.Contains(msg, "conversation window") {
				t.Errorf("expected a window-floor violation, got: %v", msg)
			}
		}()
		DigestUpdatePromptFrom(prev, in)
	})

	t.Run("a conversation quoting the tail marker does not fail a healthy prompt", func(t *testing.T) {
		// Every line pair in these turns reproduces updateSectionsMarker exactly (the
		// user line's own terminating newline supplies the marker's leading one), so
		// wherever fitTurns' clip lands, a quoted marker sits within ~70 runes of the
		// window's start. Landmark recovery therefore measured a window of roughly the
		// notice plus one line and panicked; the real window is thousands of runes.
		turns := strings.Repeat("user: quoting the harness's own output\n"+
			strings.TrimPrefix(updateSectionsMarker, "\n"), 300)
		in := RefineInput{
			SessionLabel: "work session",
			Record:       SessionRecord{Turns: 40},
			NewTurns:     turns,
		}
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("the backstop failed a HEALTHY prompt because the conversation "+
					"quoted the tail marker: %v", r)
			}
		}()
		p := DigestUpdatePromptFrom(Digest{Done: "x"}, in)
		// And the window really is healthy — measured by landmark here, which is only sound
		// because THIS assertion is allowed to be defeated by the quoting (it measures the
		// first quoted marker) and still proves the point: even that pessimistic reading
		// must be shown against the real one.
		t.Logf("total %d runes; landmark-measured window %d runes (the real one is larger — "+
			"that gap is the defect)", len([]rune(p)),
			len([]rune(promptWindow(t, p, windowHeader, updateSectionsMarker))))
	})
}

// starvingRefineInput is realisticRefineInput pushed just past what the budget can seat, so
// the conversation window lands below MinTurnChars while the assembled prompt stays INSIDE
// the budget — the silent shape, the one only the floor check catches.
//
// The extra pressure comes from SessionRecord.Projects, whose per-entry length nothing
// enforces (MaxRecordProjects bounds only the count). That is a real, currently-unbounded
// dimension rather than an invented one, and using it keeps this test independent of every
// bound task-7b did fix: none of them can absorb it, so the test cannot quietly stop
// exercising the floor check the way earlier calibrated tests did.
//
// 600 runes per project entry is scanned, not picked: at 300 the beat ladder and the view
// still yield enough room (window 1,780, both discretionary sections already shrunk), and at
// 900 the TOTAL goes over budget (14,746) so the budget check would fire first and the test
// would prove nothing about the floor. 600 is the band where only the floor check can catch
// it: window 765, total exactly at the 14,000 budget.
const starvingProjectPathLen = 600

func starvingRefineInput() RefineInput {
	in := realisticRefineInput()
	projects := make([]string, MaxRecordProjects)
	for i := range projects {
		projects[i] = pathOfLen(starvingProjectPathLen, 500+i)
	}
	in.Record.Projects = projects
	return in
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
