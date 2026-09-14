package projects

import "testing"

func TestSuggestionIDIsStableAndIndependentOfCallOrder(t *testing.T) {
	id1 := SuggestionID(SuggestKindRepo, normalizeRepo(repoKeldSignal))
	id2 := SuggestionID(SuggestKindRepo, normalizeRepo(repoKeldSignal))
	if id1 != id2 {
		t.Fatalf("SuggestionID not deterministic: %q vs %q", id1, id2)
	}
	if len(id1) != 12 {
		t.Fatalf("SuggestionID length = %d, want 12", len(id1))
	}

	// A different kind with the same value must NOT collide.
	idTicket := SuggestionID(SuggestKindTicket, normalizeRepo(repoKeldSignal))
	if idTicket == id1 {
		t.Fatalf("repo and ticket suggestions for the same string collided: %q", id1)
	}
}

func TestSuggestGroupsByRepoThenTicketThenWorkspace(t *testing.T) {
	blocks := []UnattributedBlock{
		{Dims: dimsWith(map[string]string{DimRepo: repoKeldSignal}), Minutes: 1, Tokens: 10},
		{Dims: dimsWith(map[string]string{DimBranch: "feature/KELD-9-fix"}), Minutes: 2, Tokens: 20},
		{Dims: dimsWith(map[string]string{DimWorkspace: "some-workspace"}), Minutes: 3, Tokens: 30},
	}
	got := Suggest(blocks)
	if len(got) != 3 {
		t.Fatalf("suggestions = %+v, want 3 distinct groups", got)
	}
	kinds := map[SuggestionKind]bool{}
	for _, s := range got {
		kinds[s.Kind] = true
	}
	for _, want := range []SuggestionKind{SuggestKindRepo, SuggestKindTicket, SuggestKindWorkspace} {
		if !kinds[want] {
			t.Fatalf("missing suggestion kind %q in %+v", want, got)
		}
	}
}

func TestSuggestDeterministicOrdering(t *testing.T) {
	blocks := []UnattributedBlock{
		{Dims: dimsWith(map[string]string{DimRepo: repoAtlasTSTel})},
		{Dims: dimsWith(map[string]string{DimRepo: repoKeldSignal})},
	}
	a := Suggest(blocks)
	b := Suggest(blocks)
	if len(a) != len(b) {
		t.Fatalf("non-deterministic suggestion count: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].ID != b[i].ID {
			t.Fatalf("non-deterministic suggestion order at %d: %+v vs %+v", i, a, b)
		}
	}
}
