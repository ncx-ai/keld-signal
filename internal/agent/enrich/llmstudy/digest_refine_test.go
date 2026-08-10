package llmstudy

import (
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

func TestUpdatePromptCarriesPriorDigestAndDemandsRevision(t *testing.T) {
	prev := Digest{Done: "reconciled March", Insights: []string{"ledger totals disagree"}}
	p := DigestUpdatePrompt(prev, "finance / invoicing", "user: now do April\n", "counts: turns=4 corrections=0\n")
	for _, want := range []string{"reconciled March", "ledger totals disagree", "now do April", "say what changed"} {
		if !strings.Contains(p, want) {
			t.Errorf("update prompt omits %q", want)
		}
	}
	// Recency-bias guard.
	if !strings.Contains(p, "unless the new part contradicts it") {
		t.Error("update prompt must instruct carry-forward of earlier material")
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
		Done:     "Edited agent-type-row.tsx and bumped INSTALL_SCRIPT_URL.",
		Happened: "Verified against ledger-2026-03.csv.",
	}
	p := DigestUpdatePrompt(prev, "x", "user: continue\n", "counts: turns=9\n")
	if !strings.Contains(p, "SPECIFICS ALREADY REPORTED") {
		t.Fatal("update prompt must carry an explicit retain-list")
	}
	for _, want := range []string{"agent-type-row.tsx", "INSTALL_SCRIPT_URL", "ledger-2026-03.csv"} {
		if !strings.Contains(p, want) {
			t.Errorf("retain-list omits %q", want)
		}
	}
	if !strings.Contains(p, "must not become shorter") {
		t.Error("prompt must forbid recompression, the observed cause of fact loss")
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
