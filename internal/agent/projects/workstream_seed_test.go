package projects

import "testing"

// ⚠️ **A PROJECT FILED UNDER A WORKSTREAM THAT DOES NOT EXIST IS AN INVISIBLE
// PROJECT.** The Projects pane draws projects by looping over workstreams, so a
// project whose workstream is in no list is never rendered: it exists in
// projects.json, it attributes blocks, and the person who made it sees nothing
// where their suggestion used to be.
//
// Measured on a real machine before this: two projects on disk,
// `"workstreams": null`, and a pane reading "YOUR PROJECTS" followed by nothing.
// It is the state of EVERY machine with Send to Atlas off, because the list is
// pushed down by Atlas and nothing local ever seeded it.

func suggestionFor(value string) Suggestion {
	return Suggestion{ID: SuggestionID(DimRepo, value), Kind: DimRepo, Value: value}
}

func TestBundleSeedsTheWorkstreamWhenTheOrgHasDeclaredNone(t *testing.T) {
	d := Document{Version: CurrentVersion} // no workstreams at all — Atlas off
	sug := suggestionFor("github.com/acme/web")

	next, p, err := Bundle(d, "Web", "development", []string{sug.ID}, []Suggestion{sug})
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if p.Workstream != "development" {
		t.Fatalf("project workstream = %q", p.Workstream)
	}
	var found *Workstream
	for i := range next.Workstreams {
		if next.Workstreams[i].Key == "development" {
			found = &next.Workstreams[i]
		}
	}
	if found == nil {
		t.Fatal("the project's workstream was not seeded — the project would be invisible")
	}
	if found.Name != "Development" {
		t.Fatalf("workstream name = %q, want a readable label", found.Name)
	}
	// LOCAL, never atlas: this is the machine inventing a bucket to keep its own
	// work visible, and it must not be mistaken for an org declaration.
	if found.Origin != WorkstreamOriginLocal {
		t.Fatalf("workstream origin = %q, want %q", found.Origin, WorkstreamOriginLocal)
	}
}

func TestBundleDoesNotDuplicateAWorkstreamTheOrgAlreadyDeclared(t *testing.T) {
	// NEGATIVE: a machine that DOES have org workstreams must keep grouping
	// projects under them. Matching is by key, so the org's own entry — with its
	// own name and atlas origin — is the one that stays.
	d := Document{
		Version: CurrentVersion,
		Workstreams: []Workstream{
			{Key: "development", Name: "Engineering", Origin: WorkstreamOriginAtlas},
		},
	}
	sug := suggestionFor("github.com/acme/web")

	next, _, err := Bundle(d, "Web", "development", []string{sug.ID}, []Suggestion{sug})
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if len(next.Workstreams) != 1 {
		t.Fatalf("want the org's single workstream, got %d", len(next.Workstreams))
	}
	if next.Workstreams[0].Name != "Engineering" ||
		next.Workstreams[0].Origin != WorkstreamOriginAtlas {
		t.Fatalf("the org's workstream was overwritten: %+v", next.Workstreams[0])
	}
}

func TestBundleAddsASecondWorkstreamRatherThanRenamingTheFirst(t *testing.T) {
	d := Document{
		Version:     CurrentVersion,
		Workstreams: []Workstream{{Key: "marketing", Name: "Marketing", Origin: WorkstreamOriginAtlas}},
	}
	sug := suggestionFor("github.com/acme/web")

	next, _, err := Bundle(d, "Web", "development", []string{sug.ID}, []Suggestion{sug})
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if len(next.Workstreams) != 2 {
		t.Fatalf("want both workstreams, got %+v", next.Workstreams)
	}
	if next.Workstreams[0].Key != "marketing" {
		t.Fatalf("the existing workstream must keep its place: %+v", next.Workstreams)
	}
}

func TestWorkstreamDisplayName(t *testing.T) {
	for key, want := range map[string]string{
		"development":    "Development",
		"product_design": "Product design",
		"go-to-market":   "Go to market",
		"":               "",
	} {
		if got := workstreamDisplayName(key); got != want {
			t.Fatalf("workstreamDisplayName(%q) = %q, want %q", key, got, want)
		}
	}
}
