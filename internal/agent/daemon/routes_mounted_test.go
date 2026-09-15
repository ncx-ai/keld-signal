package daemon

import (
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/atlas"
	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// ⚠️ A ROUTE THAT IS WRITTEN, TESTED AND NEVER MOUNTED IS A ROUTE THAT DOES NOT
// EXIST, and every layer can look correct while it is missing.
//
// Measured on 2026-09-15 by the phase-2 review: `integrationsReportRoute` had a
// handler, a bundle writer, a redaction gate and two planted-string tests, all
// passing — and appeared in no mux. The pane POSTed to it, the wire contract
// documented it as live, and nothing could reach it. AC-6 read green because its
// stated verification (`-run 'Report'`) happened to select a different,
// pre-existing test.
//
// This closes the class rather than the instance: the wire document is the
// contract, so every path it names must answer on a real mux. A future route
// added to the document and forgotten in the list fails here.
func TestEveryDocumentedRouteIsActuallyMounted(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/signal-integrations-wire.md")
	if err != nil {
		t.Skipf("wire doc not readable: %v", err)
	}
	// `GET /v1/integrations`, `POST /v1/integrations/{id}/setup`, ...
	re := regexp.MustCompile("`(GET|POST) (/v1/[^`]+)`")
	found := map[string]string{} // path -> method
	for _, m := range re.FindAllStringSubmatch(string(doc), -1) {
		found[m[2]] = m[1]
	}
	if len(found) == 0 {
		t.Fatal("no routes found in the wire doc; this test would pass vacuously")
	}

	mux := http.NewServeMux()
	open := func(next http.Handler) http.Handler { return next }
	for _, r := range newV3(settings.Settings{}, atlas.Off{}).routes() {
		r(mux, open)
	}

	for path, method := range found {
		// Substitute the documented placeholder with something concrete.
		probe := strings.ReplaceAll(path, "{id}", "claude_code")
		req := httptest.NewRequest(method, probe, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			t.Errorf("%s %s is documented in docs/signal-integrations-wire.md and answers 404: it is not mounted in v3.routes()", method, path)
		}
	}
}

// The mirror, so the doc cannot quietly drift the other way: a route that IS
// mounted under /v1/integrations must be documented. An undocumented route is a
// contract nobody agreed to, and the pane is written from that document.
func TestEveryMountedIntegrationsRouteIsDocumented(t *testing.T) {
	doc, err := os.ReadFile("../../../docs/signal-integrations-wire.md")
	if err != nil {
		t.Skipf("wire doc not readable: %v", err)
	}
	mux := http.NewServeMux()
	open := func(next http.Handler) http.Handler { return next }
	var probes []ingress.Route
	probes = append(probes, newV3(settings.Settings{}, atlas.Off{}).routes()...)
	for _, r := range probes {
		r(mux, open)
	}
	for _, p := range []string{"/v1/integrations", "/v1/integrations/claude_code/setup", "/v1/integrations/claude_code/report"} {
		req := httptest.NewRequest(http.MethodPost, p, strings.NewReader("{}"))
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound {
			continue // not mounted; the other test owns that
		}
		generic := strings.ReplaceAll(p, "claude_code", "{id}")
		if !strings.Contains(string(doc), generic) {
			t.Errorf("%s is mounted but not in the wire doc; the pane is written from that document", generic)
		}
	}
}
