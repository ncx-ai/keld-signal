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
	// report's own PROSE sentences may be embedded.
	for _, prose := range []string{prev.Done, prev.Happened, prev.Structure} {
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
// distinct, digit-bearing, capitalised tokens ("Id000 changed", "Id001 changed", ...).
// Every sprinkled token matches Identifiers' pattern AND its strongIdentifier fast path
// (a digit anywhere makes a token unconditional, regardless of sentence position), so each
// one is guaranteed to reach the retain-list — exercising it at a density a real, heavily
// technical report could plausibly reach, without the adversarial extreme of making EVERY
// word an identifier (measured separately: that pushes the prompt to 14,066 runes against
// an 11,000 budget — see the report for why that is flagged rather than asserted here).
func packSpecifics(n, specifics int) string {
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		if specifics > 0 && i%7 == 0 {
			specifics--
			b.WriteString(fmt.Sprintf("Id%03d changed. ", i))
			continue
		}
		b.WriteString("word ")
	}
	r := []rune(b.String())
	if len(r) > n {
		r = r[:n]
	}
	return string(r)
}

// TestRefinePromptFromWorstCaseFitsBudget is deviation 2 from the brief's dispatch: no live
// sweep here (the eval harness still calls the pre-beat entry points and is only rewired to
// RefineInput in a later task, so a sweep now would measure the OLD prompt path). Instead,
// this constructs the largest RefineInput the design can plausibly produce — every
// prior-digest section at its own cap, the full DefaultListCap of insights and unresolved
// items, all MaxBeatSelection beats at BeatCap, a maximally-populated SessionRecord, a
// focus-shift anchor (the one conditional block), a session view sized past its own cap,
// and turns far larger than any remaining room — and asserts the result still fits
// DefaultPromptCharBudget. Whether the budget (and ctx) can come down from here is the next
// task's question; this number is its input.
func TestRefinePromptFromWorstCaseFitsBudget(t *testing.T) {
	prev := Digest{
		Synopsis:  packSpecifics(DefaultSynopsisCap, 4),
		Done:      packSpecifics(DefaultProseCap, 6),
		Happened:  packSpecifics(DefaultHappenedCap, 8),
		Structure: packSpecifics(DefaultStructureCap, 10),
		Current:   packSpecifics(DefaultProseCap, 6),
		Why:       packSpecifics(DefaultProseCap, 6),
		Next:      packSpecifics(DefaultProseCap, 6),
	}
	// Insights/unresolved have no per-item rune cap in the schema — each is meant to be
	// "one per entry", a bullet-length clause, not a paragraph (real examples elsewhere in
	// this package run 20-60 runes: "ledger totals disagree", "waiting on vendor") — so 60
	// runes is already a generous worst-realistic bound, well short of BeatCap's 200 (a
	// field that IS specified at that length; these are not).
	for i := 0; i < DefaultListCap; i++ {
		prev.Insights = append(prev.Insights, packSpecifics(60, 2))
		prev.Unresolved = append(prev.Unresolved, packSpecifics(60, 2))
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
	t.Logf("worst-case refine prompt: %d runes (budget %d)", got, DefaultPromptCharBudget)
	if got > DefaultPromptCharBudget {
		t.Errorf("worst-case prompt %d runes exceeds budget %d", got, DefaultPromptCharBudget)
	}
}
