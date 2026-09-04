package ingress

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/projects"
)

// fakeBlocks is a minimal projects.BlocksSource for these tests: a fixed set
// of block summaries, standing in for the ledger store (D2) this package must
// not depend on.
type fakeBlocks struct{ rows []projects.BlockSummary }

func (f fakeBlocks) SinceWeekStart() ([]projects.BlockSummary, error) { return f.rows, nil }

func attributedDim(v string) enrich.Labeled {
	return enrich.Labeled{Value: v, Confidence: 1, Status: enrich.WorkstreamAttributed}
}

func newTestStore(t *testing.T) *projects.Store {
	t.Helper()
	t.Setenv("KELD_HOME", t.TempDir())
	return projects.NewStore(filepath.Join(t.TempDir(), "projects.json"))
}

func doRequest(t *testing.T, srv *httptest.Server, method, path, secret string, body any) *http.Response {
	t.Helper()
	var reader *strings.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = strings.NewReader(string(b))
	} else {
		reader = strings.NewReader("")
	}
	req, err := http.NewRequest(method, srv.URL+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		req.Header.Set("x-keld-agent-secret", secret)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func decodeBody(t *testing.T, res *http.Response) map[string]any {
	t.Helper()
	defer res.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return out
}

func TestProjectsRoutesRequireTheSecret(t *testing.T) {
	s := newTestStore(t)
	srv := httptest.NewServer(DiscardHandler("s3cret", ProjectsRoute(s)))
	defer srv.Close()

	res := doRequest(t, srv, http.MethodGet, "/v1/projects", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no secret: got %d, want 401", res.StatusCode)
	}

	res = doRequest(t, srv, http.MethodGet, "/v1/projects", "s3cret", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("with secret: got %d, want 200", res.StatusCode)
	}
}

func TestGetProjectsOnEmptyStoreIsHonestlyEmpty(t *testing.T) {
	s := newTestStore(t)
	srv := httptest.NewServer(DiscardHandler("s3cret", ProjectsRoute(s)))
	defer srv.Close()

	res := doRequest(t, srv, http.MethodGet, "/v1/projects", "s3cret", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	body := decodeBody(t, res)
	cov, ok := body["coverage"].(map[string]any)
	if !ok {
		t.Fatalf("coverage missing or wrong shape: %+v", body)
	}
	if cov["attributed"].(float64) != 0 || cov["total"].(float64) != 0 {
		t.Fatalf("coverage = %+v, want 0/0 with no BlocksSource wired", cov)
	}
	if body["suggestions"] != nil {
		t.Fatalf("suggestions = %+v, want none with no BlocksSource wired", body["suggestions"])
	}
}

func TestGetProjectsComputesSuggestionsFromInjectedBlocksSource(t *testing.T) {
	s := newTestStore(t)
	s.Blocks = fakeBlocks{rows: []projects.BlockSummary{
		{Dims: map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/keld-atlas")}, Minutes: 10, Tokens: 100},
	}}
	srv := httptest.NewServer(DiscardHandler("s3cret", ProjectsRoute(s)))
	defer srv.Close()

	res := doRequest(t, srv, http.MethodGet, "/v1/projects", "s3cret", nil)
	body := decodeBody(t, res)
	cov := body["coverage"].(map[string]any)
	if cov["total"].(float64) != 1 || cov["attributed"].(float64) != 0 {
		t.Fatalf("coverage = %+v, want total=1 attributed=0", cov)
	}
	sugs, ok := body["suggestions"].([]any)
	if !ok || len(sugs) != 1 {
		t.Fatalf("suggestions = %+v, want 1", body["suggestions"])
	}
}

func TestBundleRulesHidePlaceAreLocalOnly(t *testing.T) {
	s := newTestStore(t)
	s.Blocks = fakeBlocks{rows: []projects.BlockSummary{
		{Dims: map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/sdk-testbench")}, Minutes: 5, Tokens: 50},
	}}
	srv := httptest.NewServer(DiscardHandler("s3cret", ProjectsRoute(s)))
	defer srv.Close()

	// Discover the suggestion id via GET first.
	res := doRequest(t, srv, http.MethodGet, "/v1/projects", "s3cret", nil)
	body := decodeBody(t, res)
	sugs := body["suggestions"].([]any)
	if len(sugs) != 1 {
		t.Fatalf("suggestions = %+v, want 1", sugs)
	}
	sugID := sugs[0].(map[string]any)["id"].(string)

	// Bundle it.
	res = doRequest(t, srv, http.MethodPost, "/v1/projects/bundle", "s3cret", map[string]any{
		"title": "SDK work", "workstream": "development", "suggestions": []string{sugID},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("bundle status = %d", res.StatusCode)
	}
	bundleBody := decodeBody(t, res)
	if bundleBody["local_only"] != true {
		t.Fatalf("bundle response missing local_only: %+v", bundleBody)
	}
	if _, ok := bundleBody["atlas_editor_url"].(string); !ok {
		t.Fatalf("bundle response missing atlas_editor_url: %+v", bundleBody)
	}
	proj, ok := bundleBody["project"].(map[string]any)
	if !ok {
		t.Fatalf("bundle response missing project: %+v", bundleBody)
	}
	projectID := proj["id"].(string)

	// Hide it.
	res = doRequest(t, srv, http.MethodPost, "/v1/projects/"+projectID+"/hide", "s3cret", map[string]any{"hidden": true})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("hide status = %d", res.StatusCode)
	}
	hideBody := decodeBody(t, res)
	if hideBody["local_only"] != true {
		t.Fatalf("hide response missing local_only: %+v", hideBody)
	}

	// Unhide via rules is not a thing; unhide directly for the rest of the flow.
	res = doRequest(t, srv, http.MethodPost, "/v1/projects/"+projectID+"/hide", "s3cret", map[string]any{"hidden": false})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("un-hide status = %d", res.StatusCode)
	}

	// Add a rule.
	res = doRequest(t, srv, http.MethodPost, "/v1/projects/"+projectID+"/rules", "s3cret", map[string]any{
		"add": []map[string]string{{"kind": "repo", "value": "github.com/ncx-ai/atlas-telemetry-python"}},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("rules status = %d", res.StatusCode)
	}
	rulesBody := decodeBody(t, res)
	if rulesBody["local_only"] != true {
		t.Fatalf("rules response missing local_only: %+v", rulesBody)
	}

	// Unknown project -> 404.
	res = doRequest(t, srv, http.MethodPost, "/v1/projects/does-not-exist/hide", "s3cret", map[string]any{"hidden": true})
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("hide unknown project status = %d, want 404", res.StatusCode)
	}
}

func TestWorkstreamOffRouteWritesSettingsAndIsLocalOnly(t *testing.T) {
	s := newTestStore(t)
	srv := httptest.NewServer(DiscardHandler("s3cret", ProjectsRoute(s)))
	defer srv.Close()

	res := doRequest(t, srv, http.MethodPut, "/v1/workstreams/marketing/off", "s3cret", map[string]any{"off": true})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	body := decodeBody(t, res)
	if body["local_only"] != true {
		t.Fatalf("response missing local_only: %+v", body)
	}

	// Seed a document with a matching workstream and a project in it, then
	// confirm GET reflects the off flag AND excludes the project's repo from
	// attribution.
	doc := projects.Document{
		Workstreams: []projects.Workstream{{Key: "marketing", Name: "Marketing"}},
		Projects: []projects.Project{
			{ID: "p_mkt", Title: "Site", Repos: []string{"github.com/ncx-ai/keld-signal"}, Workstream: "marketing"},
		},
	}
	if err := s.Save(doc); err != nil {
		t.Fatal(err)
	}
	s.Blocks = fakeBlocks{rows: []projects.BlockSummary{
		{Dims: map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/keld-signal")}, Minutes: 1, Tokens: 1},
	}}

	res = doRequest(t, srv, http.MethodGet, "/v1/projects", "s3cret", nil)
	got := decodeBody(t, res)
	ws := got["workstreams"].([]any)[0].(map[string]any)
	if ws["off"] != true {
		t.Fatalf("workstream off flag not reflected: %+v", ws)
	}
	cov := got["coverage"].(map[string]any)
	if cov["attributed"].(float64) != 0 {
		t.Fatalf("a project in an off workstream still attributed: %+v", cov)
	}
}

func TestPlaceRefusesUnknownSuggestion(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save(projects.Document{Projects: []projects.Project{{ID: "p1", Title: "One"}}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(DiscardHandler("s3cret", ProjectsRoute(s)))
	defer srv.Close()

	res := doRequest(t, srv, http.MethodPost, "/v1/projects/place", "s3cret", map[string]any{
		"suggestion": "does-not-exist", "same_as": "p1",
	})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.StatusCode)
	}
}

// Second-round correction: a repo-shaped keyword must never appear in the
// `rules` the API returns for a project until it has actually matched a real
// observed block, and once it has, removing it (via /rules) takes it back
// out and returns its blocks to suggestions.
func TestGetProjectsRulesOnlyShowMatchedKeywords(t *testing.T) {
	s := newTestStore(t)
	if err := s.Save(projects.Document{Projects: []projects.Project{
		{ID: "p1", Title: "One", Keywords: []string{"ncx-ai/keld-signal", "design/ux"}},
	}}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(DiscardHandler("s3cret", ProjectsRoute(s)))
	defer srv.Close()

	// No blocks observed yet: the API must show p1 with no rules at all —
	// neither the repo-shaped candidate nor the free-tag one.
	res := doRequest(t, srv, http.MethodGet, "/v1/projects", "s3cret", nil)
	body := decodeBody(t, res)
	p1 := findProjectByID(t, body, "p1")
	if rules, _ := p1["rules"].([]any); len(rules) != 0 {
		t.Fatalf("rules before any observation = %+v, want none", rules)
	}

	// A block on the matching repo has been observed: the API must now show
	// the matched keyword as a rule, and never the free tag.
	s.Blocks = fakeBlocks{rows: []projects.BlockSummary{
		{Dims: map[string]enrich.Labeled{"repo": attributedDim("github.com/ncx-ai/keld-signal")}, Minutes: 5, Tokens: 50},
	}}
	res = doRequest(t, srv, http.MethodGet, "/v1/projects", "s3cret", nil)
	body = decodeBody(t, res)
	p1 = findProjectByID(t, body, "p1")
	rules, _ := p1["rules"].([]any)
	if len(rules) != 1 || rules[0] != "ncx-ai/keld-signal" {
		t.Fatalf("rules after observation = %+v, want [ncx-ai/keld-signal]", rules)
	}
	cov := body["coverage"].(map[string]any)
	if cov["attributed"].(float64) != 1 {
		t.Fatalf("block did not attribute via the matched keyword: %+v", cov)
	}

	// Remove that rule: it must disappear from `rules`, and the block must
	// return to suggestions.
	res = doRequest(t, srv, http.MethodPost, "/v1/projects/p1/rules", "s3cret", map[string]any{
		"remove": []map[string]string{{"kind": "repo", "value": "ncx-ai/keld-signal"}},
	})
	if res.StatusCode != http.StatusOK {
		t.Fatalf("remove rule status = %d", res.StatusCode)
	}

	res = doRequest(t, srv, http.MethodGet, "/v1/projects", "s3cret", nil)
	body = decodeBody(t, res)
	p1 = findProjectByID(t, body, "p1")
	if rules, _ := p1["rules"].([]any); len(rules) != 0 {
		t.Fatalf("rules after removal = %+v, want none", rules)
	}
	cov = body["coverage"].(map[string]any)
	if cov["attributed"].(float64) != 0 {
		t.Fatalf("block still attributed after the matched rule was removed: %+v", cov)
	}
	sugs, _ := body["suggestions"].([]any)
	if len(sugs) != 1 {
		t.Fatalf("block did not return to suggestions after removal: %+v", body["suggestions"])
	}
}

func findProjectByID(t *testing.T, body map[string]any, id string) map[string]any {
	t.Helper()
	for _, raw := range body["projects"].([]any) {
		p := raw.(map[string]any)
		if p["id"] == id {
			return p
		}
	}
	t.Fatalf("project %q not found in %+v", id, body["projects"])
	return nil
}
