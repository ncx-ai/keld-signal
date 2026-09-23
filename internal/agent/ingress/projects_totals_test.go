package ingress

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
)

// AC-9. Block X ($5) matches both Atlas Platform and Signal Client in Products;
// block Y ($5) matches only Atlas Platform. The route reports Products $10,
// Atlas Platform $10, Signal Client $5, and one shared block — from the same
// live pass `coverage` uses.
func TestGetProjectsReturnsTotalsCountingAGroupOnce(t *testing.T) {
	s := newTestStore(t)
	doc := projects.Document{Version: projects.CurrentVersion,
		Groups: []projects.Group{{Key: "products", Name: "Products", Origin: projects.GroupOriginLocal}},
		Projects: []projects.Project{
			{ID: "p_atlas", Title: "Atlas Platform", Group: "products", Origin: projects.OriginUser,
				Repos: []string{"github.com/ncx-ai/keld-atlas", "github.com/ncx-ai/keld-web"}},
			{ID: "p_signal", Title: "Signal Client", Group: "products", Origin: projects.OriginUser,
				Repos: []string{"github.com/ncx-ai/keld-atlas"}},
		}}
	if err := s.Save(doc); err != nil {
		t.Fatal(err)
	}
	s.Blocks = fakeBlocks{rows: []projects.BlockSummary{
		{SessionID: "x", Dims: map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/keld-atlas")}, Minutes: 20, Tokens: 100, USD: 5},
		{SessionID: "y", Dims: map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/keld-web")}, Minutes: 10, Tokens: 50, USD: 5},
	}}
	srv := httptest.NewServer(DiscardHandler("s3cret", ProjectsRoute(s)))
	defer srv.Close()

	res := doRequest(t, srv, http.MethodGet, "/v1/projects", "s3cret", nil)
	var body struct {
		Totals projects.Totals `json:"totals"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	g := body.Totals.Groups
	if len(g) != 1 || g[0].Key != "products" || g[0].USD != 10 || g[0].Blocks != 2 || g[0].SharedBlocks != 1 {
		t.Fatalf("group totals = %+v, want Products $10 over 2 blocks, 1 shared", g)
	}
	byID := map[string]float64{}
	for _, w := range body.Totals.Projects {
		byID[w.ID] = w.USD
	}
	if byID["p_atlas"] != 10 || byID["p_signal"] != 5 {
		t.Fatalf("project totals = %+v, want Atlas $10, Signal $5", body.Totals.Projects)
	}
}
