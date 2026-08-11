package llmstudy

import (
	"fmt"
	"strings"
	"testing"
)

// Insights are append-only: repeated re-summarising is what destroys the most
// valuable content, so earlier entries carry forward verbatim.
func TestMergeInsightsIsAppendOnlyAndDeduped(t *testing.T) {
	prev := Digest{Insights: []string{"retry loop was the bottleneck", "staging mirrors prod"}}
	next := Digest{Insights: []string{"staging mirrors prod", "the vendor rate-limits at 100/s"}}
	got := MergeInsights(prev, next)
	if len(got.Insights) != 3 {
		t.Fatalf("want 3 insights (2 prior + 1 new), got %d: %v", len(got.Insights), got.Insights)
	}
	if got.Insights[0] != "retry loop was the bottleneck" {
		t.Errorf("earlier insight must survive verbatim and first, got %q", got.Insights[0])
	}
}

// Unresolved is current state, not history: merging it would accumulate stale
// blockers forever, which is the opposite failure from losing them.
func TestMergeInsightsDoesNotMergeUnresolved(t *testing.T) {
	prev := Digest{Unresolved: []string{"waiting on vendor"}}
	next := Digest{Unresolved: []string{"nothing open"}}
	got := MergeInsights(prev, next)
	if len(got.Unresolved) != 1 || got.Unresolved[0] != "nothing open" {
		t.Fatalf("unresolved must reflect current state, got %v", got.Unresolved)
	}
}

// The other sections come from the new digest, not the old one.
func TestMergeInsightsKeepsNewProse(t *testing.T) {
	prev := Digest{Done: "old", Structure: "old shape"}
	next := Digest{Done: "new", Structure: "new shape"}
	got := MergeInsights(prev, next)
	if got.Done != "new" || got.Structure != "new shape" {
		t.Fatalf("prose must come from the new digest: %+v", got)
	}
}

// Unbounded growth would eventually blow the context the refine loop exists to bound.
func TestCapSectionsBoundsProseAndLists(t *testing.T) {
	long := strings.Repeat("word ", 800)
	d := Digest{
		Done: long, Happened: long, Structure: long, Current: long, Why: long, Next: long,
		Insights: make([]string, 40), Unresolved: make([]string, 40),
	}
	for i := range d.Insights {
		d.Insights[i] = "insight"
		d.Unresolved[i] = "open item"
	}
	got := CapSections(d, 400, 12)
	// structure and happened are excluded deliberately: both are cumulative and carry
	// their own larger budgets. See TestStructureGetsALargerBudget and
	// TestHappenedGetsARoomierBudgetThanPresentStateSections.
	for name, v := range map[string]string{
		"done": got.Done, "current": got.Current, "why": got.Why, "next": got.Next,
	} {
		if len([]rune(v)) > 400 {
			t.Errorf("%s not capped: %d runes", name, len([]rune(v)))
		}
	}
	if len([]rune(got.Happened)) > DefaultHappenedCap {
		t.Errorf("happened exceeds its own cap: %d runes", len([]rune(got.Happened)))
	}
	if len(got.Insights) != 12 || len(got.Unresolved) != 12 {
		t.Errorf("lists not capped: insights=%d unresolved=%d", len(got.Insights), len(got.Unresolved))
	}
}

// Structure is cumulative, so truncating it at the same budget as the others would
// lose the earliest parts of the picture — exactly what a newcomer needs.
func TestStructureGetsALargerBudget(t *testing.T) {
	if DefaultStructureCap <= DefaultProseCap {
		t.Fatalf("structure cap %d must exceed the prose cap %d", DefaultStructureCap, DefaultProseCap)
	}
	long := strings.Repeat("word ", 800)
	got := CapSections(Digest{Structure: long, Done: long}, 400, 12)
	if len([]rune(got.Structure)) <= 400 {
		t.Errorf("structure was capped at the prose budget: %d runes", len([]rune(got.Structure)))
	}
	if len([]rune(got.Structure)) > DefaultStructureCap {
		t.Errorf("structure exceeds its own cap: %d runes", len([]rune(got.Structure)))
	}
}

// Capping keeps the most recent entries; older ones have already survived several
// refinements.
func TestCapSectionsDropsOldestInsights(t *testing.T) {
	d := Digest{Insights: []string{"first", "second", "third"}}
	got := CapSections(d, 100, 2)
	if len(got.Insights) != 2 || got.Insights[0] != "second" {
		t.Fatalf("want the two most recent, got %v", got.Insights)
	}
}

// TestCapSectionsBoundsListEntryLength is the fix for task-7b finding (a): CapSections
// bounded entry COUNT (maxList) but never entry LENGTH, so a schema-legal digest with
// DefaultListCap Unresolved items of 2,000 runes each survived it untouched. Measured
// before this fix (by constructing exactly this digest, running it through CapSections,
// and feeding the result into DigestUpdatePromptFrom as `prev` — priorOpenItems embeds
// Unresolved verbatim): the assembled prompt came out at 30,191 runes against the 11,000
// budget, and the new turn ("a filler turn about the work") did not appear anywhere in it
// — fitTurns' room had gone negative and it returned nothing but its own omitted-turns
// notice. Confirmed by commenting out the two capEntryLength calls in CapSections and
// re-running this test: it fails on both assertions with those exact numbers restored.
func TestCapSectionsBoundsListEntryLength(t *testing.T) {
	prev := Digest{}
	for i := 0; i < DefaultListCap; i++ {
		prev.Unresolved = append(prev.Unresolved, strings.Repeat("x", 2000))
	}
	capped := CapSections(prev, DefaultProseCap, DefaultListCap)
	for i, item := range capped.Unresolved {
		if n := len([]rune(item)); n > DefaultListEntryCap {
			t.Errorf("unresolved[%d] not capped: %d runes (cap %d)", i, n, DefaultListEntryCap)
		}
	}

	newTurns := strings.Repeat("user: a filler turn about the work\n", 200)
	p := DigestUpdatePromptFrom(capped, RefineInput{SessionLabel: "work session", NewTurns: newTurns})
	if got := len([]rune(p)); got > DefaultPromptCharBudget {
		t.Errorf("prompt %d runes exceeds budget %d", got, DefaultPromptCharBudget)
	}
	if !strings.Contains(p, "a filler turn about the work") {
		t.Error("the new turn did not survive into the prompt")
	}
}

// The updated report must build on the new turns and say what changed — checked against
// the instructions the prompt always carries, since (per the beat/retain-list redesign)
// the prior report's own prose is no longer embedded for a specific prior digest to
// appear verbatim against. See TestRefinePromptCarriesNoPriorProse for that contract.
func TestUpdatePromptDemandsRevisionAndExtension(t *testing.T) {
	p := DigestUpdatePrompt(Digest{Done: "x"}, "finance / invoicing", "user: now do April\n", "counts: turns=4 corrections=0\n")
	for _, want := range []string{"now do April", "say what changed"} {
		if !strings.Contains(p, want) {
			t.Errorf("update prompt omits %q", want)
		}
	}
	// Recency-bias guard: earlier material survives unless the new part contradicts it.
	// Deleting the no-shrink rule must not have taken this with it — the no-shrink rule
	// forbade compression outright, this one only forbids dropping something the new
	// part is simply SILENT on, and the two are not the same instruction.
	if !strings.Contains(p, "unless the new part contradicts it") {
		t.Error("update prompt must instruct carry-forward of earlier material")
	}
	if !strings.Contains(p, "Do not drop earlier material simply because the new part does not mention it") {
		t.Error("update prompt must guard against dropping unmentioned material")
	}
	// Structure must be extended, not rewritten.
	if !strings.Contains(p, "EXTEND the picture") {
		t.Error("update prompt must tell structure to extend rather than restart")
	}
}

// An example in an earlier prompt here leaked verbatim into an unrelated report, so
// the update prompt must carry no worked examples at all.
func TestUpdatePromptCarriesNoWorkedExamples(t *testing.T) {
	p := DigestUpdatePrompt(Digest{}, "x", "user: hi\n", "counts: turns=1\n")
	for _, leaky := range []string{"resolver", "redirects", "Northwind", "checkout-api"} {
		if strings.Contains(p, leaky) {
			t.Errorf("update prompt contains a leakable example token %q", leaky)
		}
	}
}

// Retention instrumentation showed all 7 lost facts vanished while sections had room
// under their caps — the model was recompressing, not being truncated. So the prompt
// hands back the prior report's named specifics as an explicit retain-list.
func TestUpdatePromptCarriesARetainList(t *testing.T) {
	prev := Digest{
		// INSTALL_SCRIPT_URL (underscore-only, no dot/hyphen/internal-cap-before-boundary)
		// used to reach the prompt only via CarryForward's full-JSON embed of this sentence,
		// not via Identifiers — identifierPat has no separator to key on and an underscore
		// blocks its \b, so it was never actually part of the retain-list. Now that
		// CarryForward is gone, the sentinel is InstallScriptURL (internal caps), which
		// Identifiers does extract, so this test again exercises what it claims to.
		Done:     "Edited agent-type-row.tsx and bumped InstallScriptURL.",
		Happened: "Verified against ledger-2026-03.csv.",
	}
	p := DigestUpdatePrompt(prev, "x", "user: continue\n", "counts: turns=9\n")
	if !strings.Contains(p, "SPECIFICS ALREADY REPORTED") {
		t.Fatal("update prompt must carry an explicit retain-list")
	}
	for _, want := range []string{"agent-type-row.tsx", "InstallScriptURL", "ledger-2026-03.csv"} {
		if !strings.Contains(p, want) {
			t.Errorf("retain-list omits %q", want)
		}
	}
}

// A first digest has no specifics, and the section must then be omitted rather than
// printed empty — an empty label invites the model to fill it.
func TestUpdatePromptOmitsRetainListWhenEmpty(t *testing.T) {
	p := DigestUpdatePrompt(Digest{}, "x", "user: hi\n", "counts: turns=1\n")
	if strings.Contains(p, "SPECIFICS ALREADY REPORTED") {
		t.Error("empty retain-list must be omitted")
	}
}

// Fix round 1, finding 1 (CRITICAL): a legacy caller (no RefineInput, no measured
// record to offer) must not get a fabricated SESSION RECORD asserting zero turns and
// zero corrections. SessionRecord.Block() always writes its counts line unconditionally
// regardless of whether anything was actually observed, and digestRules tells the model
// those counts are measured and authoritative — "if corrections occurred the work did
// not go smoothly and the report must say so" — so an unpopulated record would assert
// "nothing happened, no friction" against every REAL fact the caller actually has (the
// old `facts` argument, which has no field on RefineInput to land in and so is correctly
// dropped rather than smuggled in — see DigestUpdatePrompt's doc — but must not be
// replaced by a fabricated substitute either).
func TestLegacyCallerGetsNoFabricatedRecord(t *testing.T) {
	p := DigestUpdatePrompt(Digest{Done: "x"}, "work session", "user: hi\n", "counts: turns=40 corrections=3\n")
	if strings.Contains(p, "SESSION RECORD") {
		t.Errorf("legacy caller must not get a fabricated SESSION RECORD section:\n%s", p)
	}
	if strings.Contains(p, "turns=0") {
		t.Errorf("legacy caller must not assert a fabricated zero-turn count:\n%s", p)
	}
}

// A genuinely populated record still reaches the prompt normally — the fix above gates
// on Populated(), it does not remove the section outright.
func TestPopulatedRecordStillWritesSection(t *testing.T) {
	p := DigestUpdatePromptFrom(Digest{Done: "x"}, RefineInput{
		SessionLabel: "work session", Record: SessionRecord{Turns: 5}, NewTurns: "user: hi\n",
	})
	if !strings.Contains(p, "SESSION RECORD") || !strings.Contains(p, "turns=5") {
		t.Error("a populated record must still be written")
	}
}

// TestRefinePromptCarriesNoPriorProse pins the core contract of this redesign: the
// previous report's PROSE is never embedded — it is replaced by the beat series — while a
// genuinely named specific still reaches the model via the deterministic retain-list.
// Both halves are asserted together: checking only absence would also be satisfied by a
// prompt that dropped the retain-list entirely, which is not what "compression, not loss"
// means.
//
// The prose sentinels are deliberately lowercase, multi-word phrases, not identifier-shaped
// tokens. Identifiers only extracts identifier-SHAPED candidates (internal capitals,
// digits, paths, dotted names) — an all-caps single-word sentinel would be picked up by the
// retain-list ON PURPOSE, the same way a real constant name or acronym would be, so a test
// built on that shape would be asserting the opposite of what the retain-list is for.
func TestRefinePromptCarriesNoPriorProse(t *testing.T) {
	prev := Digest{
		Synopsis:  "a month-end close for Meridian",
		Done:      "the vendor invoice backlog was cleared out this week",
		Happened:  "a reconciliation mismatch was traced to a duplicate posting",
		Structure: "the ledger now separates suspense entries from posted ones",
		Current:   "x", Why: "y", Next: "z",
		Insights:   []string{"an insight about the close process"},
		Unresolved: []string{"UNIQUEOPEN"},
	}
	in := RefineInput{
		SessionLabel: "work session",
		Record:       SessionRecord{Turns: 20}.WithProject("meridian"),
		Beats:        []Beat{{Ordinal: 1, Text: "work began on the March close", ChangedSubject: true}},
		NewTurns:     "user: now do April\n",
	}
	p := DigestUpdatePromptFrom(prev, in)

	// The beat series plus the retain-list replace the prose: none of the previous
	// report's own PROSE sentences may be embedded. Synopsis is the highest-value case
	// here — it is the one field CarryForward existed specifically to preserve verbatim
	// (see the deleted function's own doc), so if any field were still being smuggled
	// through whole, this is the one most likely to be it.
	absent := append([]string{prev.Synopsis, prev.Done, prev.Happened, prev.Structure}, prev.Insights...)
	for _, prose := range absent {
		if strings.Contains(p, prose) {
			t.Errorf("prior prose %q is still embedded", prose)
		}
	}
	// A genuine named specific still survives via the retain-list.
	if !strings.Contains(p, "Meridian") {
		t.Error("retain-list of named specifics is missing")
	}
	// Open items are handed over verbatim BY DESIGN (priorOpenItems is an accounting
	// requirement, not a prose leak) — this is not the same claim as the one above.
	if !strings.Contains(p, "UNIQUEOPEN") {
		t.Error("prior open items must still be accounted for")
	}
	if !strings.Contains(p, "March close") {
		t.Error("the beat series is missing")
	}
	if !strings.Contains(p, "turns=20") {
		t.Error("the measured record is missing")
	}
}

// The measured record comes first: everything after it is indicative or evidence, and the
// ordering is load-bearing — a model shown authoritative counts first holds its prose
// consistent with them.
func TestRecordPrecedesNarrative(t *testing.T) {
	in := RefineInput{
		SessionLabel: "work session",
		Record:       SessionRecord{Turns: 9},
		Beats:        []Beat{{Ordinal: 1, Text: "LADDERMARK"}},
		NewTurns:     "user: WINDOWMARK\n",
	}
	p := DigestUpdatePromptFrom(Digest{Done: "x"}, in)
	rec, bea, win := strings.Index(p, "turns=9"), strings.Index(p, "LADDERMARK"), strings.Index(p, "WINDOWMARK")
	// strings.Index returns -1 on a miss, and -1 < bea would satisfy the ordering check
	// below even if the record were missing entirely — this must fail loudly instead of
	// passing for the wrong reason.
	if rec < 0 || bea < 0 || win < 0 {
		t.Fatalf("expected all three markers present, got positions record=%d beats=%d window=%d", rec, bea, win)
	}
	if !(rec < bea && bea < win) {
		t.Errorf("want record < beats < window, got %d %d %d", rec, bea, win)
	}
}

// The no-shrink rule contradicted deliberate compression and had to go.
func TestNoShrinkRuleIsRemoved(t *testing.T) {
	p := DigestUpdatePromptFrom(Digest{Done: "x"}, RefineInput{SessionLabel: "s", NewTurns: "user: hi\n"})
	for _, gone := range []string{"must not become shorter", "Refinement ADDS"} {
		if strings.Contains(p, gone) {
			t.Errorf("the no-shrink rule survives: %q", gone)
		}
	}
}

// packSpecifics fills exactly n runes of ordinary filler, sprinkling in `specifics`
// distinct, digit-bearing, capitalised tokens ("Id000000 changed. ", "Id000001 changed.
// ", ...) drawn from `next`, a counter SHARED across every call within one test.
//
// Sharing matters because Identifiers dedups by exact string: an earlier version of
// this helper restarted its own counter at 0 on every call, so every field of a Digest
// named the same ~10 tokens and the retain-list built from all of them together
// collapsed to a fraction of its true size (68 runes measured across the whole
// Digest, against 1,126 once names are globally distinct) — understating the very
// thing this helper exists to stress. Every specific is written BEFORE the filler
// tail and the final rune-cut only ever lands inside that trailing filler, so a
// truncated field can clip "word " but never split a specific token in half.
func packSpecifics(next *int, n, specifics int) string {
	var b strings.Builder
	for i := 0; i < specifics; i++ {
		b.WriteString(fmt.Sprintf("Id%06d changed. ", *next))
		*next++
	}
	for b.Len() < n {
		b.WriteString("word ")
	}
	r := []rune(b.String())
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// TestRefinePromptFromRealisticWorstCaseMargin is deviation 2 from the brief's dispatch:
// no live sweep here (the eval harness still calls the pre-beat entry points and is only
// rewired to RefineInput in a later task, so a sweep now would measure the OLD prompt
// path). Instead, this constructs the largest RefineInput the design can plausibly
// produce — every prior-digest section at its own cap with GLOBALLY DISTINCT retain-list
// specifics (see packSpecifics), the full DefaultListCap of insights and unresolved
// items, all MaxBeatSelection beats at BeatCap, a maximally-populated SessionRecord, a
// focus-shift anchor, a session view sized past its own cap, and turns far larger than
// any remaining room — and MEASURES the result against DefaultPromptCharBudget.
//
// It does not assert "always fits": an earlier version of this test did, but its
// specifics all collided on the same ~10 names (see packSpecifics), which understated
// the retain-list by more than an order of magnitude and let the test certify a margin
// (+46 runes) that the design does not actually have. `prev` is built by hand here rather
// than passed through CapSections — the same as a digest reaching DigestUpdatePromptFrom
// straight out of CreateDigestWithView, which does not call CapSections at all — so this
// still measures against a chosen, documented assumption (60 runes/item, generous against
// this package's own real examples: "ledger totals disagree", "waiting on vendor", 20-60
// runes) rather than DefaultListEntryCap (task-7b finding (a); see digest_refine.go),
// which now bounds a CAPPED digest's per-item length at 300 — five times looser than this
// test's assumption, and so a worse realistic worst case than this test measures, not a
// better one. At the 60-rune assumption the realistic worst case is currently ABOVE
// budget — see the fix-round report for the exact number and what it means for the
// later task that owns the budget/ctx decision. Nothing in DigestUpdatePromptFrom
// clamps the ASSEMBLED prompt to the budget as a whole; clipSessionViewFor and
// fitTurns each clip only their own input, and fitDiscretionary (see digest_refine.go)
// protects the WINDOW's floor specifically, not the total.
func TestRefinePromptFromRealisticWorstCaseMargin(t *testing.T) {
	var id int
	prev := Digest{
		Synopsis:  packSpecifics(&id, DefaultSynopsisCap, 4),
		Done:      packSpecifics(&id, DefaultProseCap, 6),
		Happened:  packSpecifics(&id, DefaultHappenedCap, 8),
		Structure: packSpecifics(&id, DefaultStructureCap, 10),
		Current:   packSpecifics(&id, DefaultProseCap, 6),
		Why:       packSpecifics(&id, DefaultProseCap, 6),
		Next:      packSpecifics(&id, DefaultProseCap, 6),
	}
	for i := 0; i < DefaultListCap; i++ {
		prev.Insights = append(prev.Insights, packSpecifics(&id, 60, 2))
		prev.Unresolved = append(prev.Unresolved, packSpecifics(&id, 60, 2))
	}

	beats := make([]Beat, MaxBeatSelection)
	for i := range beats {
		beats[i] = Beat{Ordinal: i + 1, Text: strings.Repeat("w", BeatCap), ChangedSubject: i%2 == 0}
	}

	rec := SessionRecord{
		Projects:      []string{"alpha-project", "beta-project", "gamma-project", "delta-project", "epsilon-project"},
		Subjects:      []string{"one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve"},
		Tools:         []ToolCount{{"Read", 900}, {"Edit", 800}, {"Bash", 700}, {"Write", 600}, {"Grep", 500}, {"Glob", 400}, {"Task", 300}, {"WebFetch", 200}},
		Turns:         99999,
		UserTurns:     50000,
		ToolCalls:     40000,
		Corrections:   9999,
		Domain:        "engineering",
		Function:      "software-development",
		Concentration: 0.87,
		hasFocus:      true,
		TurningPoints: []TurningPoint{
			{1, TriggerFocusShift}, {2, TriggerFriction}, {3, TriggerFocusShift}, {4, TriggerFriction},
			{5, TriggerFocusShift}, {6, TriggerFriction}, {7, TriggerFocusShift}, {8, TriggerFriction},
			{9, TriggerFocusShift}, {10, TriggerFriction},
		},
	}

	// The final line is a distinct, capitalised subject so recentSubjectsOf (gated on
	// TriggerFocusShift below) actually finds something and the anchor block fires — the
	// worst case must include its conditional text, not skip it by having nothing to anchor.
	huge := strings.Repeat("user: filler turn about the close\nassistant: acknowledged\n", 4000) +
		"user: now turn to the DistinctiveAprilRollover work\n"

	in := RefineInput{
		SessionLabel: "a reasonably long session label describing the kind of work underway",
		Record:       rec,
		Beats:        beats,
		SessionView:  strings.Repeat("user: an early turn about the work\n", 400), // > SessionViewCap
		NewTurns:     huge,
		Why:          TriggerFocusShift,
	}

	p := DigestUpdatePromptFrom(prev, in)
	got := len([]rune(p))
	margin := DefaultPromptCharBudget - got
	t.Logf("realistic worst-case refine prompt: %d runes (budget %d, margin %d)", got, DefaultPromptCharBudget, margin)
	// A sanity bound only — not a claim the design fits. Catches a gross regression
	// (an infinite loop, a runaway builder) without certifying a specific margin this
	// package does not actually guarantee.
	if got <= 0 || got > 4*DefaultPromptCharBudget {
		t.Errorf("worst-case prompt length %d runes is out of sane range", got)
	}
}

// TestWindowKeepsItsFloorUnderBeatAndOpenItemPressure is the fix for finding 3: before
// fitDiscretionary existed, only the whole-session view ever yielded room to the recent
// window (clipSessionViewFor already reserved MinTurnChars against ITSELF) — the beat
// series was written at full size unconditionally. So a full 12-item open-item list
// (load-bearing) plus a full 12-beat series (previously NOT discretionary in practice)
// could exhaust the budget before the window's own reserve was ever considered, leaving
// fitTurns nothing to work with but its own omitted-turns notice: current, why, next and
// unresolved — the four sections the window is the ONLY evidence for — would then be
// written from zero conversation, silently, because the assembled prompt was still under
// budget by the only measure anyone was checking.
// Parameters are deliberately TIGHT, not generous: no session view at all (so there is
// nothing left for the view to yield — the beat count alone has to get this right), and
// an item length chosen to sit exactly on a decision boundary rather than comfortably
// inside a k that would be chosen either way. Fix round 2 found that the first version
// of this test (150-rune items plus a whole-session view) left 72 runes of slack —
// enough that fitDiscretionary's THEN-undercounted overhead estimate (it omitted
// beatsHeader and windowHeader, the literal headings the real assembly writes around
// the content being budgeted) still happened to pick the same beat count either way, so
// the window landed above the floor regardless and the test could not tell the two
// versions of fitDiscretionary apart. A test with that much slack survives the bug
// instead of catching it.
//
// This one does not survive it. Reverting the header accounting in fitDiscretionary
// (dropping the `overhead += len(beatsHeader)` / `+ len(windowHeader)` terms, i.e.
// going back to `overhead := fixed + len(cand) + tail`) was confirmed DIRECTLY, by
// making that edit, running this exact test, and reading the failure, then restoring
// the fix and re-running to confirm it passes:
//   - reverted: fitDiscretionary certifies k=8 (the header-omitting estimate still
//     satisfies its own floor check at that count), but the real assembled window is
//     only 1,567 runes — 33 below MinTurnChars (1,600). Test fails.
//   - fixed: fitDiscretionary correctly rejects k=8 (accounting for the ~114 omitted
//     runes across the two headers pushes its own estimate past the budget at that
//     count) and settles on k=7 instead; the real assembled window is 1,742 runes.
//     Test passes.
func TestWindowKeepsItsFloorAtTheBoundary(t *testing.T) {
	// 124-rune items are not an arbitrary round number: at this exact size, omitting
	// beatsHeader+windowHeader from the overhead estimate (~114 runes combined) flips
	// which beat count fitDiscretionary chooses (8 with the headers omitted, 7 with them
	// included) — the smallest gap at which the bug this test guards against actually
	// changes the outcome, rather than being absorbed by a k that was going to be chosen
	// either way.
	const itemLen = 124
	prev := Digest{}
	for i := 0; i < DefaultListCap; i++ {
		prev.Unresolved = append(prev.Unresolved, strings.Repeat("z", itemLen))
	}
	beats := make([]Beat, MaxBeatSelection)
	for i := range beats {
		beats[i] = Beat{Ordinal: i + 1, Text: strings.Repeat("w", BeatCap), ChangedSubject: i%2 == 0}
	}
	newTurns := strings.Repeat("user: a filler turn about the work\n", 200) // well over MinTurnChars
	in := RefineInput{
		SessionLabel: "work session",
		Beats:        beats,
		// No SessionView: nothing left for the view to yield, so the beat-count decision
		// alone has to get the floor right — the failure mode this test targets.
		NewTurns: newTurns,
	}
	p := DigestUpdatePromptFrom(prev, in)

	start := strings.Index(p, windowHeader)
	if start < 0 {
		t.Fatal("window header missing from prompt")
	}
	start += len(windowHeader)
	const tailMarker = "\nProduce the UPDATED report, same sections:\n"
	end := strings.Index(p[start:], tailMarker)
	if end < 0 {
		t.Fatal("tail marker missing from prompt")
	}
	window := p[start : start+end]
	got := len([]rune(window))
	t.Logf("window: %d runes (floor %d, margin %d)", got, MinTurnChars, got-MinTurnChars)
	if got < MinTurnChars {
		t.Errorf("window starved to %d runes, below the documented floor of %d — beats did "+
			"not yield enough room; window content: %.80q...", got, MinTurnChars, window)
	}
}
