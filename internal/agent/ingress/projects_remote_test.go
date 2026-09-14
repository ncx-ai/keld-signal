package ingress

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/projects"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// ⚠️ **Found by the D8 end-to-end against the real dev Atlas, not by a unit
// test.** The org served eight project values on the settings poll, the daemon
// attributed blocks against them, and GET /v1/projects answered
// `"projects": []` — because the handler returned the LOCAL document's projects
// while attributing against the merged candidate list. The page's "Your
// projects · from Atlas" section showed nothing on a machine paired with an org
// that had declared everything. This pins the merge, and the bucket derivation
// that goes with it: a remote value's bucket is its `team`, which is where
// wire_projects puts the workstream's name (docs/v3/contracts.md).
func TestGetProjectsReturnsTheOrgValuesAndTheirBuckets(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	s := projects.NewStore(filepath.Join(t.TempDir(), "projects.json"))
	s.RemoteProjects = func() []settings.RemoteProject {
		return []settings.RemoteProject{
			{ID: "keld_projects:signal", Title: "Signal On-Device Client", Team: "Keld Projects", Keywords: []string{"sidecar", "daemon"}},
			{ID: "keld_projects:atlas", Title: "Atlas Platform", Team: "Keld Projects", Keywords: []string{"fastapi"}},
			{ID: "mkt:launch", Title: "Launch", Team: "Marketing"},
		}
	}

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
		Workstreams []struct {
			Key, Name, Origin string
		} `json:"workstreams"`
		Projects []json.RawMessage `json:"projects"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Projects) != 3 {
		t.Fatalf("the org's three values must be returned as projects, got %d", len(body.Projects))
	}
	var names []string
	for _, ws := range body.Workstreams {
		if ws.Origin != projects.OriginAtlas {
			t.Fatalf("a derived bucket must carry origin %q, got %+v", projects.OriginAtlas, ws)
		}
		names = append(names, ws.Name)
	}
	if len(names) != 2 || names[0] != "Keld Projects" || names[1] != "Marketing" {
		t.Fatalf("buckets must be derived from each distinct team, once, in order: got %v", names)
	}
}

// The other direction: with no org (Atlas off, or never polled) the list is
// exactly the local document, and nothing is invented.
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
		Workstreams []json.RawMessage `json:"workstreams"`
		Projects    []json.RawMessage `json:"projects"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Projects) != 0 || len(body.Workstreams) != 0 {
		t.Fatalf("no org and an empty local document must yield nothing, got %d projects, %d workstreams",
			len(body.Projects), len(body.Workstreams))
	}
}
