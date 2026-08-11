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

	// The notice is not conversation. Both reservations upstream (clipSessionViewFor and
	// fitDiscretionary) reserve MinTurnChars PLUS omittedNotice, so the floor is a promise
	// about CONTENT — and this check used to measure the window with the notice counted in,
	// making it looser than the invariant it guards by exactly the notice's 97 runes. One
	// rune under the floor is the tightest statement of that: with the notice counted, this
	// window measures 1,696 and passes; measured as content it is 1,599 and must fail.
	t.Run("a clipped window one rune of CONTENT under the floor is flagged", func(t *testing.T) {
		window := omittedNotice + strings.Repeat("z", MinTurnChars-1)
		err := promptBudgetViolation("head"+window+"tail", window)
		if err == nil {
			t.Errorf("a window with %d runes of content (plus a %d-rune notice) passed the "+
				"%d-rune floor — the notice is being counted as conversation",
				MinTurnChars-1, len([]rune(omittedNotice)), MinTurnChars)
		} else {
			t.Logf("flagged as expected: %v", err)
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

// TestSessionLabelIsCappedOnBothPaths pins sessionLabelCap, which was unpinned on BOTH
// prompt paths — reverting either clipProse call broke no test at all.
//
// The label is fixed overhead written ahead of everything else each path budgets around, and
// nothing about a caller's label is under this package's control. Measured before the cap
// existed: a 12,000-rune label alone produced a 15,954-rune create prompt.
//
// Asserted on the label's own text rather than on the prompt's total length, so it stays a
// test of THIS bound: with the cap reverted, the uncapped label is present verbatim, which
// the run-length check catches directly instead of via whichever budget it happens to blow.
// The panic path is caught too, because at 12,000 runes the reverted assembly trips the
// backstop before returning — and a bare panic mid-suite is not a legible failure. Confirmed
// by reverting both clipProse calls: create fails at 15,954 runes and refine at 17,944, both
// over the 14,000 budget (15,954 is the same figure the round-3 review measured).
func TestSessionLabelIsCappedOnBothPaths(t *testing.T) {
	label := strings.Repeat("L", 12000)
	overCap := strings.Repeat("L", sessionLabelCap+1)

	build := map[string]func() string{
		"create": func() string {
			return DigestCreatePrompt(label, "user: hi\n", "counts: turns=1\n")
		},
		"refine": func() string {
			return DigestUpdatePrompt(Digest{Done: "x"}, label, "user: hi\n", "counts: turns=1\n")
		},
	}
	for name, f := range build {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("an uncapped session label blew the prompt budget: %v", r)
				}
			}()
			if p := f(); strings.Contains(p, overCap) {
				t.Errorf("session label reached the prompt at over %d runes uncapped", sessionLabelCap)
			}
		})
	}
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
//
// EVERY SUBTEST RUNS TWICE, ASCII and multi-byte, and that is the whole reason this test
// still exists in its own right rather than as a duplicate of the worst-case test. Its
// earlier form was ASCII-only, and the round-3 review found it certifying the exact opposite
// of the truth: the assemblies charged their prefix in BYTES while the reservations were in
// RUNES, which is invisible in ASCII (the two counts are equal) and broke on 2% of real
// mined transcripts, where an em dash or an arrow makes them differ. That was the FIFTH test
// on this branch certifying a bound the code did not enforce, so the pairing is structural:
// nonASCII rewrites the conversation ONE CHARACTER FOR ONE into the multi-byte characters
// these transcripts really contain, so the two runs are the same text at the same RUNE
// length and must therefore produce the SAME window — asserted directly, not just "both
// clear the floor". Confirmed by reverting runeLen back to b.Len() in the two assemblies:
// the ASCII refine run passes at 1,604 runes of content while the multi-byte one PANICS at
// 1,510 — the same shape the real corpus produced — and on the create path, where the floor
// happens to survive, the equality assertion catches it anyway: 1,768 runes of content in
// ASCII against 1,602 in multi-byte, a 166-rune difference from the identical text. That is
// why the pairing asserts equality and not merely "both clear the floor": one of the two
// paths only breached, and the other only differed.
//
// (The 11,000-budget figures above were measured with the ASCII-only version, so they are the
// ASCII run's numbers; the capacity decision they justify is unaffected — the byte/rune bug
// made the window smaller than the budget allowed, never larger.)
func TestFloorIsReachableAtRealisticInputScale(t *testing.T) {
	for _, f := range flavours {
		t.Run("refine, record at realistic scale, "+f.name, func(t *testing.T) {
			defer failOnBackstop(t)
			p := DigestUpdatePromptFrom(realisticPrev(), realisticRefineInputLines(f.line()))
			assertFitsAndKeepsTheFloor(t, p, windowHeader, updateSectionsMarker)
			f.record(len([]rune(windowOf(promptWindow(t, p, windowHeader, updateSectionsMarker)))))
		})
	}
	assertSameWindowInBothEncodings(t, "refine")

	for _, f := range flavours {
		t.Run("create, large measured-facts block, "+f.name, func(t *testing.T) {
			defer failOnBackstop(t)
			facts := realisticFactsBlock()
			line := f.line()
			t.Logf("facts block: %d runes; conversation line %d runes / %d bytes",
				len([]rune(facts)), len([]rune(line)), len(line))
			p := DigestCreatePromptWithView("work session",
				strings.Repeat(line, 400), strings.Repeat(line, 400), facts)
			assertFitsAndKeepsTheFloor(t, p, createWindowHeader, createSectionsMarker)
			f.record(len([]rune(windowOf(promptWindow(t, p, createWindowHeader, createSectionsMarker)))))
		})
	}
	assertSameWindowInBothEncodings(t, "create")
}

// nonASCII rewrites ASCII punctuation into the multi-byte characters this corpus actually
// contains, ONE RUNE FOR ONE: an em dash, an arrow, a check mark, a curly apostrophe. Rune
// count identical, byte count larger — which is exactly the difference the prompt budget was
// getting wrong, and nothing else about the input changes.
var nonASCII = strings.NewReplacer("-", "—", ">", "→", "*", "✓", "'", "’")

// realisticTurnLine is one conversation line for the realistic-scale test, carrying the
// ordinary prose punctuation real turns carry — the characters nonASCII has something to
// rewrite. realisticRefineInput's own default line is deliberately left alone (other tests
// are calibrated against it), so this pairing adds a variant rather than moving anyone's
// baseline.
const realisticTurnLine = "user: an early turn - about the work, 2 of 3 > next\n"

// flavours is the ASCII/multi-byte pair every subtest above runs under, plus the recorder
// that lets the two runs be compared with each other rather than only against the floor.
var flavours = []struct {
	name   string
	line   func() string
	record func(int)
}{
	{"ascii", func() string { return realisticTurnLine }, func(n int) { measuredWindows["ascii"] = n }},
	{"multi-byte", func() string { return nonASCII.Replace(realisticTurnLine) },
		func(n int) { measuredWindows["multi-byte"] = n }},
}

var measuredWindows = map[string]int{}

// assertSameWindowInBothEncodings is the invariant the ASCII/multi-byte pairing exists to
// state: two inputs of identical RUNE length must yield windows of identical rune length,
// because the budget is denominated in runes. A difference means something in the arithmetic
// is counting bytes.
func assertSameWindowInBothEncodings(t *testing.T, path string) {
	t.Helper()
	a, m := measuredWindows["ascii"], measuredWindows["multi-byte"]
	if a == 0 || m == 0 {
		t.Fatalf("%s: a flavour did not report its window (ascii %d, multi-byte %d)", path, a, m)
	}
	if a != m {
		t.Errorf("%s: same text at the same rune length produced different windows — ascii %d "+
			"runes of content, multi-byte %d (difference %d). The budget is in runes; something "+
			"is charging bytes.", path, a, m, a-m)
	}
	measuredWindows = map[string]int{}
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

// assertFitsAndKeepsTheFloor checks the two invariants the backstop checks, on the same
// quantities: the whole prompt against the budget, and the window's CONTENT — omittedNotice
// stripped, via windowOf — against MinTurnChars.
//
// Measuring the notice as part of the window was worth 97 runes of phantom margin, which is
// the same off-by-the-notice this round fixed in promptBudgetViolation itself. A test that
// reports a looser margin than the code enforces is how a graze gets read as comfortable.
func assertFitsAndKeepsTheFloor(t *testing.T, p, marker, tail string) {
	t.Helper()
	total := len([]rune(p))
	window := len([]rune(windowOf(promptWindow(t, p, marker, tail))))
	t.Logf("total %d runes (budget %d, margin %d), window content %d (floor %d, margin %d)",
		total, DefaultPromptCharBudget, DefaultPromptCharBudget-total,
		window, MinTurnChars, window-MinTurnChars)
	if total > DefaultPromptCharBudget {
		t.Errorf("prompt %d runes exceeds the %d-rune budget", total, DefaultPromptCharBudget)
	}
	if window < MinTurnChars {
		t.Errorf("window starved to %d runes of content, below the %d-rune floor", window, MinTurnChars)
	}
}

// realisticPrev is a steady-state prior report: every prose section at its own cap and a
// full list of insights/unresolved at DefaultListEntryCap — i.e. exactly what CapSections
// returns, with identifier-dense content so the retain-list is at its bound too. densePrev
// (digest_refine_test.go) is the shared builder; the worst-case test uses it with a larger
// open-item count, so the two constructions cannot drift apart.
func realisticPrev() Digest { return densePrev(DefaultListCap) }

// realisticRefineInput is one refinement of a long engineering session: a full record
// (subjects and projects path-shaped, tools, focus, turning points at their cap), a full
// beat ladder, an over-cap session view, and a focus shift whose newest turn names files.
func realisticRefineInput() RefineInput {
	return realisticRefineInputLines("user: an early turn about the work\n")
}

// realisticRefineInputLines is realisticRefineInput with the conversation and view built from
// a caller-chosen line, so the same input can be assembled in two encodings of the same text
// (see nonASCII and TestFloorIsReachableAtRealisticInputScale). Everything else is identical
// between the two, which is what makes the comparison a controlled one.
func realisticRefineInputLines(line string) RefineInput {
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

	// Beat text is built from the same line as the conversation, at exactly BeatCap RUNES.
	// It has to be, for the ASCII/multi-byte pairing to mean anything: the beat ladder is the
	// only one of these inputs that reaches the assembled PREFIX (the view yields to zero at
	// this input scale, and the turns are what is being budgeted, not overhead), so beats
	// written as strings.Repeat("w", BeatCap) left the prefix pure ASCII in both runs and the
	// multi-byte variant could not see the defect at all. Rune-exact slicing rather than
	// clipProse, so both encodings are the same length to the rune.
	beatText := string([]rune(strings.Repeat(strings.TrimSuffix(line, "\n")+" ", 40))[:BeatCap])
	beats := make([]Beat, MaxBeatSelection)
	for i := range beats {
		beats[i] = Beat{Ordinal: i + 1, Text: beatText, ChangedSubject: i%2 == 0}
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
		SessionView:  strings.Repeat(line, 400), // > SessionViewCap
		NewTurns:     strings.Repeat(line, 400) + last.String(),
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
// view), a big enough block starves the window at ANY budget.
//
// RE-MEASURED once the floor was read as CONTENT rather than content-plus-notice (this
// round's notice-accounting fix), by padding this block one rune at a time against the
// worst-case create construction: the floor HOLDS all the way to 8,267 runes of facts and
// breaches at 8,268 ("clipped to 1,599 runes of content"). Up to that point clipSessionViewFor
// keeps handing the window exactly its reserve, so the content margin sits at +1 to +3 the
// whole way rather than degrading — the reserve working as designed, not a graze. It breaks
// at the point the view has nothing left to yield. The earlier note here read "~6,000 leaves
// the floor intact with room to spare, ~8,800 does not (window 1,357)": the first half was
// measuring the notice as conversation (at 5,971 runes of facts the real content margin is
// +2, not +99) and the second was 500 runes optimistic. Bounding DigestFacts' two unbounded
// lists is the fix; it is not in task-7b's scope, and the backstop makes the failure loud
// rather than silent in the meantime.
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

// TestFitTurnsLineBoundaryTrimCannotBreachTheFloor is the fix for task-7b finding (b), and
// for the AMPLIFIER the round-3 review found still living inside that fix.
//
// After slicing to `room` runes, fitTurns trims back to the next '\n' so the window does not
// open mid-word. The original version did that UNCONDITIONALLY, discarding up to one whole
// line beyond what `room` computed, so the window could land below MinTurnChars even though
// `room` cleared it. Both shapes from the brief are exercised in the first two subtests —
// many short lines (a small, compounding overshoot) and one long pasted turn straddling the
// cut (a near-total collapse, because the first '\n' the trim finds is that turn's own
// terminator, deep into the clipped window).
//
// overhead is chosen so the room LEFT FOR CONTENT after fitTurns' own internal reservation for
// omittedNotice (97 runes) lands at exactly MinTurnChars — no slack at all, the tightest
// boundary at which the bug can bite. (Using DefaultPromptCharBudget-MinTurnChars directly as
// the overhead would test a confound instead: the notice reservation alone already eats 97
// runes out of that room regardless of trim behaviour, so the pre-trim content would already
// sit below the floor and the test could not isolate the trim bug from that separate, expected
// reservation.) Any slack beyond this exact boundary lets the naive trim's loss be absorbed
// without the floor actually being breached, and a test with that much slack survives the bug
// instead of catching it — the same lesson TestWindowKeepsItsFloorAtTheBoundary documents for
// fitDiscretionary.
//
// Confirmed by reverting the guard to an unconditional trim and re-running:
//   - many short lines: window measured 1,575 runes, 25 below the 1,600 floor. Fails.
//   - one long line straddling the cut: window measured 11 runes, 1,589 below. Fails.
//
// The THIRD subtest is the amplifier, and it is the one the first two could not see. The
// guard as first written was `room < MinTurnChars || runeLen(kept) >= MinTurnChars`: it also
// trimmed unconditionally once `room` was ALREADY below the floor, reasoning that there was
// then "nothing left to protect". Both subtests above sit at room == MinTurnChars exactly,
// which is the one value at which that clause is dormant — so a one-rune-worse room, the
// shape the byte/rune mismatch actually produced, fell through the whole test. Measured
// across the boundary: room 1,600 gives 1,600 runes of content, room 1,599 gives 1,200. A
// one-rune miss cost 400 runes here, and up to PerTurnChars (1,200) on a mined turn.
// Reverting the `room < MinTurnChars ||` clause fails it at exactly those numbers.
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

	// The amplifier: room one rune BELOW the floor must cost one rune of content, not a
	// whole line. Lines are PerTurnChars-shaped because that is the real upper bound on how
	// much one mined turn can cost (window.go clips each turn to it), so this measures the
	// worst real loss and not an invented one.
	t.Run("room one rune short of the floor loses one rune, not a line", func(t *testing.T) {
		const lineLen = 400 // 4 lines of these straddle the boundary being probed
		var b strings.Builder
		for i := 0; i < 40; i++ {
			b.WriteString(strings.Repeat("a", lineLen-1))
			b.WriteString("\n")
		}
		turns := b.String()

		at := len([]rune(windowOf(fitTurns(turns, overhead))))
		below := len([]rune(windowOf(fitTurns(turns, overhead+1))))
		t.Logf("room %d -> content %d; room %d -> content %d (loss %d)",
			MinTurnChars, at, MinTurnChars-1, below, at-below)
		if at-below > 1 {
			t.Errorf("one rune less room cost %d runes of content (room %d gave %d, room %d "+
				"gave %d) — the boundary trim amplifies a small miss into a whole line, and "+
				"it is at exactly this point that the window is MOST worth protecting",
				at-below, MinTurnChars, at, MinTurnChars-1, below)
		}
	})
}
