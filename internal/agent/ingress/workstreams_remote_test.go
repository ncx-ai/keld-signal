package ingress

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/agent/workstreams"
)

// ⚠️ **Found by the D8 end-to-end against the real dev Atlas, not by a unit
// test.** The org served eight project values on the settings poll, the daemon
// attributed blocks against them, and GET /v1/workstreams answered
// `"projects": []` — because the handler returned the LOCAL document's projects
// while attributing against the merged candidate list. The page's "Your
// projects · from Atlas" section showed nothing on a machine paired with an org
// that had declared everything. This pins the merge, and the bucket derivation
// that goes with it: a remote value's bucket is its `team`, which is where
// wire_projects puts the workstream's name (docs/v3/contracts.md).
func TestGetWorkstreamsReturnsTheOrgValuesAndTheirBuckets(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	s := workstreams.NewStore(filepath.Join(t.TempDir(), "projects.json"))
	s.RemoteWorkstreams = func() []settings.RemoteWorkstream {
		return []settings.RemoteWorkstream{
			{ID: "keld_projects:signal", Title: "Signal On-Device Client", Team: "Keld Projects", Keywords: []string{"sidecar", "daemon"}},
			{ID: "keld_projects:atlas", Title: "Atlas Platform", Team: "Keld Projects", Keywords: []string{"fastapi"}},
			{ID: "mkt:launch", Title: "Launch", Team: "Marketing"},
		}
	}

	mux := http.NewServeMux()
	WorkstreamsRoute(s)(mux, func(h http.Handler) http.Handler { return h })
	srv := httptest.NewServer(mux)
	defer srv.Close()

	res, err := http.Get(srv.URL + "/v1/workstreams")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Groups []struct {
			Key, Name, Origin string
		} `json:"groups"`
		Workstreams []json.RawMessage `json:"workstreams"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Workstreams) != 3 {
		t.Fatalf("the org's three values must be returned as workstreams, got %d", len(body.Workstreams))
	}
	var names []string
	for _, ws := range body.Groups {
		if ws.Origin != workstreams.OriginAtlas {
			t.Fatalf("a derived bucket must carry origin %q, got %+v", workstreams.OriginAtlas, ws)
		}
		names = append(names, ws.Name)
	}
	if len(names) != 2 || names[0] != "Keld Projects" || names[1] != "Marketing" {
		t.Fatalf("buckets must be derived from each distinct team, once, in order: got %v", names)
	}
}

// The other direction: with no org (Atlas off, or never polled) the list is
// exactly the local document, and nothing is invented.
func TestGetWorkstreamsWithoutAnOrgIsTheLocalDocumentOnly(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	s := workstreams.NewStore(filepath.Join(t.TempDir(), "projects.json"))
	mux := http.NewServeMux()
	WorkstreamsRoute(s)(mux, func(h http.Handler) http.Handler { return h })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	res, err := http.Get(srv.URL + "/v1/workstreams")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body struct {
		Groups      []json.RawMessage `json:"groups"`
		Workstreams []json.RawMessage `json:"workstreams"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Workstreams) != 0 || len(body.Groups) != 0 {
		t.Fatalf("no org and an empty local document must yield nothing, got %d workstreams, %d groups",
			len(body.Workstreams), len(body.Groups))
	}
}
