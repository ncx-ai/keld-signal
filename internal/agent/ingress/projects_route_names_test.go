// vocab:keep-file — asserts Revision 1's route is gone.
package ingress

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// R3-AC-2. Signal's bucket is a project again (2026-09-25): the pane is served at
// /v1/projects under `projects`, and Revision 1's /v1/workstreams is not
// mounted. The page is embedded in the daemon, so no alias is kept.
func TestTheRoutesSayProject(t *testing.T) {
	s := newTestStore(t)
	srv := httptest.NewServer(DiscardHandler("s3cret", ProjectsRoute(s)))
	defer srv.Close()

	res := doRequest(t, srv, http.MethodGet, "/v1/projects", "s3cret", nil)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/projects = %d", res.StatusCode)
	}
	body := decodeBody(t, res)
	for _, k := range []string{"projects", "suggestions", "coverage", "totals"} {
		if _, ok := body[k]; !ok {
			t.Fatalf("GET /v1/projects has no %q: %+v", k, body)
		}
	}
	if _, ok := body["workstreams"]; ok {
		t.Fatalf("the pane must not carry Revision 1's `workstreams` key: %+v", body)
	}
	for _, p := range []string{"/v1/workstreams", "/v1/workstreams/bundle"} {
		m := http.MethodGet
		if p != "/v1/workstreams" {
			m = http.MethodPost
		}
		if r := doRequest(t, srv, m, p, "s3cret", map[string]any{}); r.StatusCode != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404", m, p, r.StatusCode)
		}
	}
}

// The link the page offers for the org-wide edit is ATLAS's page, and Atlas calls
// it workstreams. The Revision 3 rename flipped this to /projects once and no
// test noticed, so it is pinned here: a project is Signal's word, not Atlas's.
func TestTheAtlasEditorLinkIsAtlassWorkstreamsPage(t *testing.T) {
	if u := atlasEditorURL(); !strings.HasSuffix(u, "/workstreams") {
		t.Fatalf("atlas_editor_url = %q, want Atlas's /workstreams page", u)
	}
}
