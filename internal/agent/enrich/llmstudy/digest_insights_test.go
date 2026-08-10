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
