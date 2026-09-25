package ingress

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
)

// R4-AC-2. Signal has only projects, in one flat list (Revision 4,
// 2026-09-25). The catalog carries exactly four keys and no `groups`; no
// project view names a group; the totals are per project only; and the route
// that switched a group off is gone.
func TestTheCatalogHasNoGroups(t *testing.T) {
	s := newTestStore(t)
	// Stored the way 3.0.6 stores it: every project under a declared group,
	// one of them another group. None of that may reach the API.
	if err := s.Save(projects.Document{Version: projects.CurrentVersion,
		Groups: []projects.Group{
			{Key: "products", Name: "Products", Origin: projects.GroupOriginLocal},
			{Key: "q3", Name: "Q3", Origin: projects.GroupOriginLocal},
		},
		Projects: []projects.Project{
			{ID: "p_atlas", Title: "Atlas", Group: "products", Origin: projects.OriginUser,
				Repos: []string{"github.com/ncx-ai/keld-atlas"}},
			{ID: "p_launch", Title: "Launch", Group: "q3", Origin: projects.OriginUser,
				Repos: []string{"github.com/ncx-ai/keld-atlas"}},
		}}); err != nil {
		t.Fatal(err)
	}
	s.Blocks = fakeBlocks{rows: []projects.BlockSummary{
		{SessionID: "x", Dims: map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/keld-atlas")}, Minutes: 20, USD: 5},
	}}
	srv := httptest.NewServer(DiscardHandler("s3cret", ProjectsRoute(s)))
	defer srv.Close()

	res := doRequest(t, srv, http.MethodGet, "/v1/projects", "s3cret", nil)
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var body map[string]json.RawMessage
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("decode: %v\n%s", err, raw)
	}
	var keys []string
	for k := range body {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if strings.Join(keys, ",") != "coverage,projects,suggestions,totals" {
		t.Fatalf("catalog keys = %v, want exactly coverage, projects, suggestions, totals", keys)
	}

	var views []map[string]any
	if err := json.Unmarshal(body["projects"], &views); err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("projects = %s", body["projects"])
	}
	for _, v := range views {
		if _, ok := v["group"]; ok {
			t.Fatalf("a project view must not name a group: %v", v)
		}
		if _, ok := v["workstream"]; ok { // vocab:keep — 3.0.6's stored key must not leak either
			t.Fatalf("the stored group key must not reach the API: %v", v)
		}
	}

	var totals map[string]json.RawMessage
	if err := json.Unmarshal(body["totals"], &totals); err != nil {
		t.Fatal(err)
	}
	if len(totals) != 1 || totals["projects"] == nil {
		t.Fatalf("totals = %s, want only `projects`", body["totals"])
	}
	var rows []map[string]any
	if err := json.Unmarshal(totals["projects"], &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if _, ok := r["group"]; ok {
			t.Fatalf("a project total must not name a group: %v", r)
		}
		if _, ok := r["shared_blocks"]; ok {
			t.Fatalf("shared_blocks was a group figure and must be gone: %v", r)
		}
	}

	if res := doRequest(t, srv, http.MethodPut, "/v1/groups/products/off", "s3cret",
		map[string]bool{"off": true}); res.StatusCode != http.StatusNotFound {
		t.Fatalf("PUT /v1/groups/{key}/off = %d, want 404 (the route is removed)", res.StatusCode)
	}
}

// R4-AC-3. Two projects on the same repository, three blocks on it: BOTH
// projects hold every block in full (blocks, minutes, usd), coverage counts
// each block ONCE, and the live ledger cell — the one the Today rows show —
// names both ids.
func TestASharedBlockCountsInEveryProjectAndOnceInCoverage(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save(projects.Document{Version: projects.CurrentVersion,
		Projects: []projects.Project{
			{ID: "p_atlas", Title: "Atlas", Origin: projects.OriginUser,
				Repos: []string{"github.com/ncx-ai/keld-atlas"}},
			{ID: "p_billing", Title: "Billing", Origin: projects.OriginUser,
				Repos: []string{"ncx-ai/keld-atlas"}},
		}}); err != nil {
		t.Fatal(err)
	}
	repo := map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/keld-atlas")}
	s.Blocks = fakeBlocks{rows: []projects.BlockSummary{
		{SessionID: "a", Dims: repo, Minutes: 20, Tokens: 100, USD: 2},
		{SessionID: "b", Dims: repo, Minutes: 10, Tokens: 50, USD: 3},
		{SessionID: "c", Dims: repo, Minutes: 5, Tokens: 10, USD: 1},
		{SessionID: "d", Dims: map[string]enrich.Labeled{"repo": attributedDim("github.com/other/thing")}, Minutes: 7, USD: 9},
	}}
	srv := httptest.NewServer(DiscardHandler("s3cret", ProjectsRoute(s)))
	defer srv.Close()

	res := doRequest(t, srv, http.MethodGet, "/v1/projects", "s3cret", nil)
	var body struct {
		Totals   projects.Totals `json:"totals"`
		Coverage struct {
			Attributed int `json:"attributed"`
			Total      int `json:"total"`
		} `json:"coverage"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	res.Body.Close()

	if body.Coverage.Attributed != 3 || body.Coverage.Total != 4 {
		t.Fatalf("coverage = %+v, want 3 of 4 — a shared block counts once", body.Coverage)
	}
	byID := map[string]projects.ProjectTotal{}
	for _, p := range body.Totals.Projects {
		byID[p.ID] = p
	}
	for _, id := range []string{"p_atlas", "p_billing"} {
		p := byID[id]
		if p.Blocks != 3 || p.Minutes != 35 || p.Tokens != 160 || p.USD != 6 {
			t.Fatalf("%s total = %+v, want every shared block in full: 3 blocks, 35 min, 160 tokens, $6", id, p)
		}
	}

	pass, err := NewAttribution(s)
	if err != nil {
		t.Fatal(err)
	}
	cell := AttributedCell(pass.Of(repo), time.Now().UTC().Format(time.RFC3339))
	list, _ := cell["projects"].([]map[string]any)
	var ids []string
	for _, e := range list {
		if _, ok := e["group"]; ok {
			t.Fatalf("a live cell entry must not name a group: %v", e)
		}
		ids = append(ids, e["project_id"].(string))
	}
	sort.Strings(ids)
	if cell["status"] != "ok" || strings.Join(ids, ",") != "p_atlas,p_billing" {
		t.Fatalf("live cell = %v, want ok naming both projects", cell)
	}
}
