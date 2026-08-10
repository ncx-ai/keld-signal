package llmstudy

import (
	"strings"
	"testing"
)

// Defect 1, observed in a real digest: `done` ended mid-word, "...saved as
// `worktree-cleanup-blocke". Structurally valid JSON, so every threshold passed it, but
// a manager reading it sees a broken sentence.
func TestClipEndsOnAWordBoundary(t *testing.T) {
	s := "the migration was saved as worktree-cleanup-blockers and then reviewed"
	got := clipProse(s, 32)
	if strings.HasSuffix(got, "blocke") || strings.Contains(got, "blocke ") {
		t.Fatalf("clipped mid-word: %q", got)
	}
	if len([]rune(got)) > 32 {
		t.Errorf("clip exceeded the budget: %d runes", len([]rune(got)))
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("a clipped section must show it was clipped: %q", got)
	}
}

// A sentence end is preferred when it costs almost nothing.
func TestClipPrefersANearbySentenceEnd(t *testing.T) {
	s := "The layout was paired with the devices table. Then the card grew taller."
	got := clipProse(s, 47)
	if !strings.HasSuffix(got, "devices table.") {
		t.Errorf("want a clip at the nearby sentence end, got %q", got)
	}
}

// A DISTANT sentence end must NOT be preferred: discarding a third of a section to end
// tidily measurably deleted the evidence that rubberstamp detection depends on, taking it
// from 0% to 11.1%.
func TestClipDoesNotDiscardContentToEndTidily(t *testing.T) {
	s := "Short first. " + strings.Repeat("then more detail about the reversal ", 12)
	got := clipProse(s, 300)
	if strings.HasSuffix(got, "Short first.") {
		t.Fatalf("clip discarded most of the section to end on a sentence: %q", got)
	}
	if len([]rune(got)) < 270 {
		t.Errorf("clip kept only %d of a 300-rune budget", len([]rune(got)))
	}
}

func TestClipLeavesShortProseAlone(t *testing.T) {
	if got := clipProse("short enough", 100); got != "short enough" {
		t.Errorf("short prose was altered: %q", got)
	}
}

// Defect 2, observed in a real digest: insights 6 and 9 were the same sentence differing
// only by a leading "The". Exact-match dedup let both through.
func TestMergeInsightsDropsNearDuplicates(t *testing.T) {
	prev := Digest{Insights: []string{
		"The Docker-based dev stack introduces an operational gotcha: the main checkout lacks node_modules.",
	}}
	next := Digest{Insights: []string{
		"Docker-based dev stack introduces an operational gotcha: the main checkout lacks node_modules.",
		"A genuinely different learning about topological stage ordering.",
	}}
	got := MergeInsights(prev, next).Insights
	if len(got) != 2 {
		t.Fatalf("want the duplicate collapsed, got %d: %v", len(got), got)
	}
	if !strings.Contains(strings.Join(got, " "), "topological") {
		t.Error("the genuinely new insight was dropped")
	}
}

// Rewording must not defeat it either — that is how a restatement actually arrives.
func TestMergeInsightsDropsRewordedRestatements(t *testing.T) {
	prev := Digest{Insights: []string{"Users prefer a clear separation between KPIs and diagnostics."}}
	next := Digest{Insights: []string{"Users prefer clear separation between the KPIs and the diagnostics."}}
	if got := MergeInsights(prev, next).Insights; len(got) != 1 {
		t.Fatalf("want the restatement collapsed, got %d: %v", len(got), got)
	}
}

// Genuinely distinct insights that share vocabulary must both survive — the dedup must
// not become a content filter.
func TestMergeInsightsKeepsDistinctInsightsSharingWords(t *testing.T) {
	prev := Digest{Insights: []string{"Pill components ensure visual consistency across pages."}}
	next := Digest{Insights: []string{"Pill components misrepresent the data type for built-in status."}}
	if got := MergeInsights(prev, next).Insights; len(got) != 2 {
		t.Fatalf("want both kept, got %d: %v", len(got), got)
	}
}

// A possessive on only ONE side must not defeat the match: wordsOf splits "Meridian's" into
// "meridian" and a lone "s", and before significantWords stripped the apostrophe up front,
// stemming that lone "s" left an empty string counted only against the non-possessive side's
// word count — dragging a genuine restatement (0.714) below insightMatchRatio (0.8).
func TestInsightsMatchAcrossPossessive(t *testing.T) {
	a := "The work is reconciling the March ledger for Meridian."
	b := "Work continues reconciling Meridian's March ledger."
	if !insightsMatch(a, b) {
		t.Error("a possessive rewording was not recognized as the same insight")
	}
}

// The same apostrophe-bearing word on BOTH sides is the ordinary case for a contraction
// repeated across two rewordings, not an edge case — and it moves the ratio the OTHER way
// from the asymmetric case above. Unfixed, "charlie's" on both sides contributed a phantom ""
// that was shared AND counted in both word sets: shared=4, larger=5, ratio=0.8 — exactly at
// insightMatchRatio's threshold, so these two genuinely distinct insights (delta vs echo)
// were wrongly called a match. Fixed: shared=3 (alpha, bravo, charlie — real words only),
// larger=4, ratio=0.75, correctly below threshold.
func TestInsightsMatchDoesNotInflateOnSharedPossessive(t *testing.T) {
	a := "alpha bravo charlie's delta"
	b := "alpha bravo charlie's echo"
	if insightsMatch(a, b) {
		t.Error("a shared possessive artifact inflated two distinct insights into a false match")
	}
}

// The duplicate-collapse call site (MergeInsights' dedup, not just insightsMatch in
// isolation) must benefit from the possessive fix too.
func TestMergeInsightsCollapsesPossessiveRewording(t *testing.T) {
	prev := Digest{Insights: []string{"The work is reconciling the March ledger for Meridian."}}
	next := Digest{Insights: []string{"Work continues reconciling Meridian's March ledger."}}
	if got := MergeInsights(prev, next).Insights; len(got) != 1 {
		t.Fatalf("want the possessive restatement collapsed, got %d: %v", len(got), got)
	}
}

// Defect 3, observed in a real digest: insight 7 stated the user's feedback BACKWARDS
// (claiming a preference for pill components when the session reversed away from them),
// and append-only merging meant it could never be removed. Retirement is the way out.
func TestRetiredInsightsAreDropped(t *testing.T) {
	prev := Digest{Insights: []string{
		"Pill components are required for built-in status.",
		"Topological ordering validates stage dependencies.",
	}}
	next := Digest{Insights: []string{"Free-text fields are required for built-in status."}}
	got := mergeWithRetirement(prev, next, []string{"Pill components are required for built-in status."})
	joined := strings.Join(got.Insights, " | ")
	if strings.Contains(joined, "Pill components are required") {
		t.Errorf("the retired insight survived: %s", joined)
	}
	if !strings.Contains(joined, "Topological") || !strings.Contains(joined, "Free-text") {
		t.Errorf("retirement removed more than it was asked to: %s", joined)
	}
}

// Retirement is a drift risk: a model free to delete history could quietly erase it. It
// is bounded, so a refinement can correct a mistake but not rewrite the record.
func TestRetirementIsBounded(t *testing.T) {
	prev := Digest{Insights: []string{"one alpha", "two beta", "three gamma", "four delta"}}
	got := mergeWithRetirement(prev, Digest{}, prev.Insights)
	if len(got.Insights) != len(prev.Insights)-maxRetiredPerRefinement {
		t.Fatalf("want exactly %d retired, got %d remaining of %d",
			maxRetiredPerRefinement, len(got.Insights), len(prev.Insights))
	}
}

// A retirement naming something that is not in the list must change nothing, rather than
// silently dropping the closest match.
func TestUnmatchedRetirementIsIgnored(t *testing.T) {
	prev := Digest{Insights: []string{"one alpha", "two beta"}}
	got := mergeWithRetirement(prev, Digest{}, []string{"something never said"})
	if len(got.Insights) != 2 {
		t.Errorf("an unmatched retirement changed the list: %v", got.Insights)
	}
}

// The refinement schema must actually offer retirement, or the merge support is dead code.
func TestUpdateSchemaOffersRetirement(t *testing.T) {
	sc := DigestUpdateSchema()
	props, ok := sc["properties"].(map[string]any)
	if !ok || props["retired"] == nil {
		t.Fatal("refinement schema has no retired field")
	}
	var found bool
	for _, r := range sc["required"].([]string) {
		if r == "retired" {
			found = true
		}
	}
	if !found {
		t.Error("retired must be required, so the model considers it every refinement")
	}
	// The base schema must stay clean: retirement instructs the merge, it is not report
	// content, and it must not appear in a stored or published digest.
	if base, _ := DigestSchema()["properties"].(map[string]any); base["retired"] != nil {
		t.Error("the base digest schema must not carry retired")
	}
}

// Retirement must be for correcting the record, not tidying it, so the prompt has to say
// so — an unqualified invitation to prune is how history gets quietly erased.
func TestUpdatePromptConstrainsRetirement(t *testing.T) {
	p := DigestUpdatePrompt(Digest{Done: "x", Insights: []string{"an insight"}},
		"work session", "user: next\n", "counts: turns=2\n")
	for _, want := range []string{"retired", "WRONG or was reversed", "not for tidying"} {
		if !strings.Contains(p, want) {
			t.Errorf("refine prompt omits %q", want)
		}
	}
}

// Clipping must be idempotent: `done` and `happened` are carried forward, the model
// copies the ellipsis, and a non-idempotent clip appended another — real digests read
// "increasing from……".
func TestClipDoesNotAccumulateEllipses(t *testing.T) {
	once := clipProse(strings.Repeat("some prose about the close process ", 40), 300)
	twice := clipProse(once, 300)
	if strings.Contains(twice, "……") {
		t.Fatalf("re-clipping accumulated ellipses: %q", twice[len(twice)-40:])
	}
	if twice != once {
		t.Errorf("clip is not idempotent:\n once: %q\ntwice: %q", once, twice)
	}
}

// Happened is cumulative AND carries the record of difficulty, so it must not be capped
// as tightly as the present-state sections — clipping keeps the oldest text, so a tight
// cap silently deletes the most recent reversal.
func TestHappenedGetsARoomierBudgetThanPresentStateSections(t *testing.T) {
	if DefaultHappenedCap <= DefaultProseCap {
		t.Fatalf("happened cap %d must exceed the prose cap %d", DefaultHappenedCap, DefaultProseCap)
	}
	long := strings.Repeat("word ", 700)
	got := CapSections(Digest{Happened: long, Current: long}, DefaultProseCap, DefaultListCap)
	if len([]rune(got.Happened)) <= DefaultProseCap {
		t.Errorf("happened was capped at the prose budget: %d runes", len([]rune(got.Happened)))
	}
	if len([]rune(got.Current)) > DefaultProseCap {
		t.Errorf("current exceeded the prose budget: %d runes", len([]rune(got.Current)))
	}
}

// A carried section can hold a marker MID-text from an earlier clip. When the new clip
// lands just after it the two become adjacent — a real digest read "increasing from……".
func TestClipDoesNotDoubleAnInteriorMarker(t *testing.T) {
	s := "the provision increased from… " + strings.Repeat("and then more detail ", 30)
	got := clipProse(s, 34)
	if strings.Contains(got, "……") {
		t.Fatalf("interior marker was doubled: %q", got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("clipped text must still be marked: %q", got)
	}
}
