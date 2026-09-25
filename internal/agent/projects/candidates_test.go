package projects

import "testing"

// Revision 2 (2026-09-25): Signal attributes only to the projects people
// define in Signal. The candidate set is the local document and nothing else;
// an overlay made by "Same as" is a local entry, so it is in it.
func TestCandidatesAreTheSignalDocumentOnly(t *testing.T) {
	d := Document{Projects: []Project{
		{ID: "p_platform", Title: "Platform", Group: "products", Repos: []string{"github.com/acme/a"}, Origin: OriginUser},
		{ID: "atlas-7", Title: "Billing", Group: "features", Repos: []string{"github.com/acme/b"}, Origin: OriginAtlas},
	}}
	got := Candidates(d)
	if len(got) != 2 || got[0].ID != "p_platform" || got[1].ID != "atlas-7" {
		t.Fatalf("Candidates = %+v, want the document's two entries in order", got)
	}
	// A copy: a caller appending to the result must not write into the document.
	got = append(got[:1], Project{ID: "x"})
	if d.Projects[1].ID != "atlas-7" {
		t.Fatalf("Candidates aliased the document's slice")
	}
	if n := len(Candidates(Document{})); n != 0 {
		t.Fatalf("empty document gave %d candidates", n)
	}
}
