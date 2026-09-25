// vocab:keep-file — reads 3.0.6's stored keys (workstreams, workstream) off disk.
package projects

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// ⚠️ **A PROJECT FILED UNDER A GROUP THE FILE DOES NOT DECLARE IS AN INVISIBLE
// PROJECT — IN 3.0.6.** 3.0.6 draws projects by looping over the file's
// groups, so a project whose group is in no list is never rendered there: it
// exists in projects.json, it attributes blocks, and a person whose machine
// was auto-updated back to 3.0.6 sees nothing.
//
// Measured on a real machine before the first fix: two projects on disk,
// `"workstreams": null`, and a pane reading "YOUR PROJECTS" followed by nothing.
//
// Until Revision 4 Bundle seeded the group it was asked to file under. Signal
// has no groups now, so a new project carries none and SAVE is what files it
// (toStored) — these tests pin that on the bytes 3.0.6 reads.

func suggestionFor(value string) Suggestion {
	return Suggestion{ID: SuggestionID(DimRepo, value), Kind: DimRepo, Value: value}
}

type stored306 struct {
	Workstreams []Group `json:"workstreams"`
	Projects    []struct {
		ID         string `json:"id"`
		Workstream string `json:"workstream"`
	} `json:"projects"`
}

func saveAndRead(t *testing.T, d Document) stored306 {
	t.Helper()
	path := filepath.Join(t.TempDir(), FileName)
	if err := Save(path, d); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var s stored306
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func bundled(t *testing.T, d Document) Document {
	t.Helper()
	sug := suggestionFor("github.com/acme/web")
	next, p, err := Bundle(d, "Web", []string{sug.ID}, []Suggestion{sug})
	if err != nil {
		t.Fatalf("Bundle: %v", err)
	}
	if p.Group != "" {
		t.Fatalf("a new project carries no group since Revision 4, got %q", p.Group)
	}
	return next
}

func TestSaveFilesAGrouplessProjectUnderOneInternalGroup(t *testing.T) {
	s := saveAndRead(t, bundled(t, Document{Version: CurrentVersion})) // no groups at all
	if len(s.Workstreams) != 1 || s.Workstreams[0] != InternalGroup {
		t.Fatalf("want exactly the internal group declared, got %+v", s.Workstreams)
	}
	if InternalGroup.Key != "projects" || InternalGroup.Name != "Projects" || InternalGroup.Origin != GroupOriginLocal {
		t.Fatalf("the internal group is {projects, Projects, local}: %+v", InternalGroup)
	}
	if len(s.Projects) != 1 || s.Projects[0].Workstream != "projects" {
		t.Fatalf("the project must be filed under it: %+v", s.Projects)
	}
}

func TestSaveFilesAGrouplessProjectUnderTheFirstDeclaredGroupAndTouchesNoGroup(t *testing.T) {
	// NEGATIVE: a document that declares groups gets NO internal group, and the
	// person's own groups are written back exactly as read.
	org := []Group{
		{Key: "development", Name: "Engineering", Origin: GroupOriginAtlas},
		{Key: "marketing", Name: "Marketing", Origin: GroupOriginLocal, Off: true},
	}
	s := saveAndRead(t, bundled(t, Document{Version: CurrentVersion, Groups: org}))
	if len(s.Workstreams) != 2 || s.Workstreams[0] != org[0] || s.Workstreams[1] != org[1] {
		t.Fatalf("the person's groups must be untouched, got %+v", s.Workstreams)
	}
	if len(s.Projects) != 1 || s.Projects[0].Workstream != "development" {
		t.Fatalf("the project must be filed under the first declared group: %+v", s.Projects)
	}
}

func TestSaveKeepsAStoredGroupAndDeclaresItIfTheFileDoesNot(t *testing.T) {
	// A project already stored under a group keeps it; one naming a group the
	// file does not declare (an overlay that copied it before Revision 4) gets
	// that group declared, after the person's own, rather than being moved.
	d := Document{
		Version: CurrentVersion,
		Groups:  []Group{{Key: "marketing", Name: "Marketing", Origin: GroupOriginLocal}},
		Projects: []Project{
			{ID: "p_site", Title: "Site", Group: "marketing"},
			{ID: "p_overlay", Title: "Atlas", Group: "keld-products"},
		},
	}
	s := saveAndRead(t, d)
	if len(s.Workstreams) != 2 || s.Workstreams[0].Key != "marketing" ||
		s.Workstreams[1].Key != "keld-products" || s.Workstreams[1].Name != "Keld products" {
		t.Fatalf("want marketing kept first and keld-products declared: %+v", s.Workstreams)
	}
	if s.Projects[0].Workstream != "marketing" || s.Projects[1].Workstream != "keld-products" {
		t.Fatalf("each project keeps its stored group: %+v", s.Projects)
	}
	// And the in-memory document is not changed by saving it.
	if len(d.Groups) != 1 {
		t.Fatalf("Save must not mutate the caller's document: %+v", d.Groups)
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
