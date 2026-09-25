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

func TestBundleSeedsTheGroupWhenTheOrgHasDeclaredNone(t *testing.T) {
	d := Document{Version: CurrentVersion} // no groups at all — Atlas off
	sug := suggestionFor("github.com/acme/web")

	next, p, err := Bundle(d, "Web", "development", []string{sug.ID}, []Suggestion{sug})
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if p.Group != "development" {
		t.Fatalf("project project = %q", p.Group)
	}
	var found *Group
	for i := range next.Groups {
		if next.Groups[i].Key == "development" {
			found = &next.Groups[i]
		}
	}
	if found == nil {
		t.Fatal("the project's group was not seeded — the project would be invisible")
	}
	if found.Name != "Development" {
		t.Fatalf("group name = %q, want a readable label", found.Name)
	}
	// LOCAL, never atlas: this is the machine inventing a bucket to keep its own
	// work visible, and it must not be mistaken for an org declaration.
	if found.Origin != GroupOriginLocal {
		t.Fatalf("project origin = %q, want %q", found.Origin, GroupOriginLocal)
	}
}

func TestBundleDoesNotDuplicateAGroupTheOrgAlreadyDeclared(t *testing.T) {
	// NEGATIVE: a machine that DOES have org workstreams must keep grouping
	// projects under them. Matching is by key, so the org's own entry — with its
	// own name and atlas origin — is the one that stays.
	d := Document{
		Version: CurrentVersion,
		Groups: []Group{
			{Key: "development", Name: "Engineering", Origin: GroupOriginAtlas},
		},
	}
	sug := suggestionFor("github.com/acme/web")

	next, _, err := Bundle(d, "Web", "development", []string{sug.ID}, []Suggestion{sug})
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if len(next.Groups) != 1 {
		t.Fatalf("want the org's single project, got %d", len(next.Groups))
	}
	if next.Groups[0].Name != "Engineering" ||
		next.Groups[0].Origin != GroupOriginAtlas {
		t.Fatalf("the org's workstream was overwritten: %+v", next.Groups[0])
	}
}

func TestBundleAddsASecondGroupRatherThanRenamingTheFirst(t *testing.T) {
	d := Document{
		Version: CurrentVersion,
		Groups:  []Group{{Key: "marketing", Name: "Marketing", Origin: GroupOriginAtlas}},
	}
	sug := suggestionFor("github.com/acme/web")

	next, _, err := Bundle(d, "Web", "development", []string{sug.ID}, []Suggestion{sug})
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if len(next.Groups) != 2 {
		t.Fatalf("want both projects, got %+v", next.Groups)
	}
	if next.Groups[0].Key != "marketing" {
		t.Fatalf("the existing project must keep its place: %+v", next.Groups)
	}
}

func TestGroupDisplayName(t *testing.T) {
	for key, want := range map[string]string{
		"development":    "Development",
		"product_design": "Product design",
		"go-to-market":   "Go to market",
		"":               "",
	} {
		if got := groupDisplayName(key); got != want {
			t.Fatalf("groupDisplayName(%q) = %q, want %q", key, got, want)
		}
	}
}
