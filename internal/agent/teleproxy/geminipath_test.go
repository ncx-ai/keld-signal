package teleproxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/telemetry"
)

// ⚠️ **THE URL IN THIS TEST IS THE ONE GEMINI ACTUALLY BUILDS, NOT ONE THIS
// PACKAGE FINDS CONVENIENT.** That distinction is the whole history of this
// file: the proxy's own tests used to post to the header name this package
// chose, all three tools were 401'd live, and every test stayed green. So the
// endpoint is composed here exactly as gemini-cli does it — plain string
// concatenation of the signal path onto whatever telemetry.GeminiTelemetry
// wrote — and the assertion is that the proxy accepts THAT.
func geminiURL(t *testing.T, base, token, signal string) string {
	t.Helper()
	p := telemetry.SetupParams{Endpoint: base, IngestToken: token}
	tm := telemetry.GeminiTelemetry(p)
	v, _ := tm.Get("otlpEndpoint")
	endpoint, _ := v.(string)
	if endpoint == "" {
		t.Fatal("GeminiTelemetry wrote no otlpEndpoint")
	}
	// gemini-cli: `${endpoint}${signal}`. No URL parsing, no query preservation.
	return endpoint + signal
}

func TestProxyAcceptsTheURLGeminiBuilds(t *testing.T) {
	p := New("http://atlas.invalid/v1/logs", "http://atlas.invalid/v1/metrics",
		func() string { return "atlas-token" }, "SECRET", t.TempDir())
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()

	for _, signal := range []string{"/v1/logs", "/v1/metrics", "/v1/traces"} {
		url := geminiURL(t, srv.URL, "SECRET", signal)
		if strings.Contains(url, "?") {
			t.Fatalf("%s: the endpoint still carries a query string (%q) — that is "+
				"the form the SDK destroys", signal, url)
		}
		resp, err := http.Post(url, "application/x-protobuf", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("%s: %v", signal, err)
		}
		resp.Body.Close()
		// 2xx: the forwarding routes answer 202 (the tool is answered before
		// Atlas is), /v1/traces answers 200. What matters is that neither is the
		// 404/401 pair gemini used to get.
		if resp.StatusCode/100 != 2 {
			t.Errorf("%s -> %d, want 2xx. Measured on gemini-cli 0.37.1 before the "+
				"path form: 404 then 401 on every single export.", signal, resp.StatusCode)
		}
	}
	if p.TracesDropped() != 1 {
		t.Errorf("traces dropped = %d, want 1 — a discarded signal must be counted",
			p.TracesDropped())
	}
}

// A wrong token in the path is refused, or the route is an open door.
func TestProxyRefusesAWrongPathToken(t *testing.T) {
	p := New("http://atlas.invalid/v1/logs", "http://atlas.invalid/v1/metrics",
		func() string { return "atlas-token" }, "SECRET", t.TempDir())
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()

	for _, u := range []string{
		srv.URL + telemetry.GeminiTokenPath + "WRONG/v1/logs",
		srv.URL + telemetry.GeminiTokenPath + "WRONG/v1/traces",
	} {
		resp, err := http.Post(u, "application/x-protobuf", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s -> %d, want 401", u, resp.StatusCode)
		}
	}
}

// ⚠️ The QUERY form is still accepted although nothing writes it. A tool reads
// its config once at startup and an upgrade preserves config files, so a machine
// configured by an older release keeps posting the old shape; refusing it would
// add a second outage to the one it already has.
func TestProxyStillAcceptsTheLegacyQueryToken(t *testing.T) {
	p := New("http://atlas.invalid/v1/logs", "http://atlas.invalid/v1/metrics",
		func() string { return "atlas-token" }, "SECRET", t.TempDir())
	srv := httptest.NewServer(p.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/logs?token=SECRET",
		"application/x-protobuf", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		t.Errorf("legacy ?token= -> %d, want 2xx", resp.StatusCode)
	}
}
