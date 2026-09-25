package ingress

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// catalogBody is the part of GET /v1/projects these tests read.
type catalogBody struct {
	Projects []struct {
		ID     string   `json:"id"`
		Title  string   `json:"title"`
		Origin string   `json:"origin"`
		Rules  []string `json:"rules"`
	} `json:"projects"`
	Totals   projects.Totals `json:"totals"`
	Coverage struct {
		Attributed int `json:"attributed"`
		Total      int `json:"total"`
	} `json:"coverage"`
}

func getCatalog(t *testing.T, s *projects.Store) catalogBody {
	t.Helper()
	srv := httptest.NewServer(DiscardHandler("s3cret", ProjectsRoute(s)))
	defer srv.Close()
	res := doRequest(t, srv, http.MethodGet, "/v1/projects", "s3cret", nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/projects = %d", res.StatusCode)
	}
	var body catalogBody
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

// R2-AC-2. ⚠️ **THIS REVERSES THE TEST THAT USED TO LIVE HERE**
// (TestGetProjectsReturnsTheOrgValuesAndTheirBuckets, from the D8
// end-to-end), which pinned the org's values AS the page's projects and
// derived "from Atlas" group headings from their teams. Revision 2
// (2026-09-25): Signal attributes only to projects defined in Signal. The
// org's list is still HELD — the getter below is set and would answer — but
// the catalog, the totals and the coverage count are computed without it, so
// an Atlas workstream on the very repo a Signal project claims neither
// shows up nor turns that block into a conflict, and a block only an Atlas
// project would have matched is unattributed.
func TestTheCatalogHoldsOnlySignalProjects(t *testing.T) {
	s := newTestStore(t)
	s.RemoteProjects = func() []settings.RemoteProject {
		return []settings.RemoteProject{
			// Same repo as the Signal project: must not share its blocks.
			{ID: "keld_products:atlas", Title: "Atlas Platform", Team: "Keld Products",
				Repos: []string{"github.com/ncx-ai/keld-signal"}},
			// A group the document lacks, on a repo only it names.
			{ID: "mkt:launch", Title: "Launch", Team: "Marketing",
				Repos: []string{"github.com/ncx-ai/launch-site"}},
		}
	}
	if err := s.Save(projects.Document{Version: projects.CurrentVersion,
		Groups: []projects.Group{{Key: "products", Name: "Products", Origin: projects.GroupOriginLocal}},
		Projects: []projects.Project{
			{ID: "w_signal", Title: "Signal Client", Group: "products", Origin: projects.OriginUser,
				Repos: []string{"github.com/ncx-ai/keld-signal"}},
		}}); err != nil {
		t.Fatal(err)
	}
	s.Blocks = fakeBlocks{rows: []projects.BlockSummary{
		{SessionID: "a", Dims: map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/keld-signal")}, Minutes: 20, Tokens: 100, USD: 4},
		{SessionID: "b", Dims: map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/launch-site")}, Minutes: 10, Tokens: 50, USD: 3},
	}}

	body := getCatalog(t, s)
	if len(body.Projects) != 1 || body.Projects[0].ID != "w_signal" {
		t.Fatalf("catalog projects = %+v, want exactly the Signal project", body.Projects)
	}
	if body.Coverage.Total != 2 || body.Coverage.Attributed != 1 {
		t.Fatalf("coverage = %+v, want 1 of 2: only the Signal project's block", body.Coverage)
	}
	if len(body.Totals.Projects) != 1 || body.Totals.Projects[0].ID != "w_signal" ||
		body.Totals.Projects[0].USD != 4 || body.Totals.Projects[0].Blocks != 1 {
		t.Fatalf("project totals = %+v, want Signal Client $4 over 1 block", body.Totals.Projects)
	}

	// The live pass (what the ledger's attributed cells are drawn from) agrees.
	pass, err := NewAttribution(s)
	if err != nil {
		t.Fatal(err)
	}
	if res := pass.Of(map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/launch-site")}); res.Attributed() ||
		res.Reason != projects.ReasonNoRuleMatched {
		t.Fatalf("a block only an Atlas workstream matches = %#v, want no_rule_matched", res)
	}
	if ids := pass.Of(map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/keld-signal")}).IDs(); len(ids) != 1 || ids[0] != "w_signal" {
		t.Fatalf("the shared-repo block = %v, want the Signal project alone (no conflict)", ids)
	}
}

// R2-AC-6. An overlay — the entry "Same as" laid down for an Atlas value
// before Revision 2 — is a Signal project now. It keeps its stored origin
// and Atlas id in the document (project_matches still carries that id), but
// the catalog reports it as the person's own. (It also used to get a local
// heading for its undeclared group; there are no headings since Revision 4.)
func TestAnOverlayIsASignalProject(t *testing.T) {
	s := newTestStore(t)
	doc := projects.Document{Version: projects.CurrentVersion,
		Projects: []projects.Project{
			{ID: "keld_products:atlas", Title: "Atlas Platform", Team: "Keld Products", Group: "keld-products",
				Origin: projects.OriginAtlas, Repos: []string{"github.com/ncx-ai/keld-atlas"}},
		}}
	if err := s.Save(doc); err != nil {
		t.Fatal(err)
	}
	s.Blocks = fakeBlocks{rows: []projects.BlockSummary{
		{SessionID: "a", Dims: map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/keld-atlas")}, Minutes: 20, Tokens: 100, USD: 6},
	}}

	body := getCatalog(t, s)
	if len(body.Projects) != 1 {
		t.Fatalf("catalog projects = %+v, want the overlay", body.Projects)
	}
	w := body.Projects[0]
	if w.ID != "keld_products:atlas" || w.Title != "Atlas Platform" || w.Origin != projects.OriginUser {
		t.Fatalf("overlay view = %+v, want its id and title with origin %q", w, projects.OriginUser)
	}
	if len(w.Rules) != 1 || w.Rules[0] != "github.com/ncx-ai/keld-atlas" {
		t.Fatalf("overlay rules = %v, want its one repo", w.Rules)
	}
	if len(body.Totals.Projects) != 1 || body.Totals.Projects[0].ID != "keld_products:atlas" || body.Totals.Projects[0].USD != 6 {
		t.Fatalf("totals = %+v, want the overlay's block counted under it", body.Totals.Projects)
	}

	pass, err := NewAttribution(s)
	if err != nil {
		t.Fatal(err)
	}
	if ids := pass.Of(map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/keld-atlas")}).IDs(); len(ids) != 1 || ids[0] != "keld_products:atlas" {
		t.Fatalf("live attribution = %v, want the overlay", ids)
	}

	// The view is a copy: the stored origin (and so the Atlas id reaching
	// project_matches) is untouched by being displayed.
	stored, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if stored.Projects[0].Origin != projects.OriginAtlas {
		t.Fatalf("stored origin = %q, want it left %q", stored.Projects[0].Origin, projects.OriginAtlas)
	}
}

// "Same as" can only place a suggestion onto a project Signal holds. Before
// Revision 2 an Atlas-only id was a valid target and laid an overlay down; now
// it is simply not a project this machine knows.
func TestSameAsOffersOnlySignalProjects(t *testing.T) {
	s := newTestStore(t)
	s.RemoteProjects = func() []settings.RemoteProject {
		return []settings.RemoteProject{{ID: "mkt:launch", Title: "Launch", Team: "Marketing"}}
	}
	s.Blocks = fakeBlocks{rows: []projects.BlockSummary{
		{Dims: map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/launch-site")}, Minutes: 5, Tokens: 50},
	}}
	srv := httptest.NewServer(DiscardHandler("s3cret", ProjectsRoute(s)))
	defer srv.Close()

	sugs := decodeBody(t, doRequest(t, srv, http.MethodGet, "/v1/projects", "s3cret", nil))["suggestions"].([]any)
	if len(sugs) != 1 {
		t.Fatalf("suggestions = %+v, want 1", sugs)
	}
	sugID := sugs[0].(map[string]any)["id"].(string)

	res := doRequest(t, srv, http.MethodPost, "/v1/projects/place", "s3cret", map[string]any{
		"suggestion": sugID, "same_as": "mkt:launch",
	})
	if got := decodeBody(t, res); res.StatusCode != http.StatusNotFound || got["error"] != "project_not_found" {
		t.Fatalf("place onto an Atlas-only id = %d %+v, want 404 project_not_found", res.StatusCode, got)
	}
	d, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Projects) != 0 {
		t.Fatalf("a refused place wrote %+v", d.Projects)
	}
}

// Map-to likewise targets only what Signal holds.
func TestMapToOnlySignalProjects(t *testing.T) {
	s := newTestStore(t)
	s.RemoteProjects = func() []settings.RemoteProject {
		return []settings.RemoteProject{{ID: "mkt:launch", Title: "Launch", Team: "Marketing"}}
	}
	if err := s.Save(projects.Document{Version: projects.CurrentVersion,
		Projects: []projects.Project{{ID: "w1", Title: "Mine", Origin: projects.OriginUser,
			Repos: []string{"github.com/ncx-ai/launch-site"}}}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(DiscardHandler("s3cret", ProjectsRoute(s)))
	defer srv.Close()
	res := doRequest(t, srv, http.MethodPost, "/v1/projects/w1/same-as", "s3cret", map[string]any{"same_as": "mkt:launch"})
	if got := decodeBody(t, res); res.StatusCode != http.StatusNotFound || got["error"] != "project_not_found" {
		t.Fatalf("map onto an Atlas-only id = %d %+v, want 404 project_not_found", res.StatusCode, got)
	}
}

// With no org (Atlas off, or never polled) the list is exactly the local
// document, and nothing is invented.
func TestGetProjectsWithoutAnOrgIsTheLocalDocumentOnly(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	s := projects.NewStore(filepath.Join(t.TempDir(), "projects.json"))
	mux := http.NewServeMux()
	ProjectsRoute(s)(mux, func(h http.Handler) http.Handler { return h })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/v1/projects")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Projects []json.RawMessage `json:"projects"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Projects) != 0 {
		t.Fatalf("no org and an empty local document must yield nothing, got %d projects",
			len(body.Projects))
	}
}
