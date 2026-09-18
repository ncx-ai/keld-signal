package mockatlas

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/config"
)

func newTestServer(t *testing.T, opts Options) (*httptest.Server, *Server) {
	t.Helper()
	if opts.StateDir == "" {
		opts.StateDir = t.TempDir()
	}
	srv, err := New(opts)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts, srv
}

func do(t *testing.T, method, url string, body string, headers map[string]string) *http.Response {
	t.Helper()
	var r io.Reader
	if body != "" {
		r = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, r)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func counts(t *testing.T, ts *httptest.Server) map[string]int {
	t.Helper()
	resp := do(t, "GET", ts.URL+"/_conform/counts", "", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("counts status = %d", resp.StatusCode)
	}
	var out struct {
		Counts map[string]int `json:"counts"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode counts: %v", err)
	}
	return out.Counts
}

// ---- the login path the harness drives ----

// TestLoginWithCodeThenOnboardingYieldsAUsableHookConfig runs the REAL client
// code `keld login --code` runs — api.Client.Enroll, auth.LoginWithCode,
// api.Client.Onboarding, config.SaveHookConfig — against the mock, and ends
// where the daemon starts: a hook.json whose endpoint resolves to this mock.
func TestLoginWithCodeThenOnboardingYieldsAUsableHookConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	ts, _ := newTestServer(t, Options{})

	c := api.NewClient(ts.URL, "")
	ad, err := auth.LoginWithCode(c, "TEST")
	if err != nil {
		t.Fatalf("LoginWithCode: %v", err)
	}
	if ad.AccessToken == "" || ad.Org == "" || ad.Principal == "" {
		t.Fatalf("auth data = %+v, want a token, principal and org", ad)
	}
	if _, err := os.Stat(filepath.Join(home, "auth.json")); err != nil {
		t.Fatalf("auth.json not written: %v", err)
	}

	ob, err := api.NewClient(ts.URL, ad.AccessToken).Onboarding()
	if err != nil {
		t.Fatalf("Onboarding: %v", err)
	}
	if ob.IngestToken == "" {
		t.Fatalf("onboarding returned no ingest token: %+v", ob)
	}
	// The endpoint must carry a "/v1/" segment, because every publish URL the
	// daemon builds is `endpoint[:index("/v1/")] + "/v1/<route>"`. An endpoint
	// without it would send every publish to the wrong host silently.
	if !strings.Contains(ob.Endpoint, "/v1/") {
		t.Fatalf("endpoint %q has no /v1/ segment; publish URL derivation would misfire", ob.Endpoint)
	}
	if !strings.HasPrefix(ob.Endpoint, ts.URL) {
		t.Fatalf("endpoint %q does not point at the mock (%s)", ob.Endpoint, ts.URL)
	}

	if err := config.SaveHookConfig(ob.Endpoint, ob.IngestToken); err != nil {
		t.Fatalf("SaveHookConfig: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(home, "hook.json"))
	if err != nil {
		t.Fatalf("hook.json: %v", err)
	}
	var hook struct {
		Endpoint    string `json:"endpoint"`
		IngestToken string `json:"ingest_token"`
	}
	if err := json.Unmarshal(raw, &hook); err != nil {
		t.Fatalf("hook.json is not JSON: %v", err)
	}
	if hook.IngestToken != ob.IngestToken || hook.Endpoint != ob.Endpoint {
		t.Fatalf("hook.json = %+v, want the onboarding values", hook)
	}

	// And that token is the one the publish routes accept.
	resp := do(t, "POST", ts.URL+"/v1/signal/blocks", `{"blocks":[]}`,
		map[string]string{"x-keld-ingest-token": hook.IngestToken})
	if resp.StatusCode >= 400 {
		t.Fatalf("publish with the onboarding token = %d, want < 400", resp.StatusCode)
	}
}

func TestDeviceFlowPollsPendingThenAuthorizes(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	ts, _ := newTestServer(t, Options{PollPending: 2})

	c := api.NewClient(ts.URL, "")
	var seen string
	ad, err := auth.Login(c, false, func(time.Duration) {}, func(string) error { return nil },
		func(ds *api.DeviceStart) { seen = ds.UserCode })
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if seen == "" {
		t.Errorf("device start reported no user code")
	}
	if ad.AccessToken == "" {
		t.Errorf("device flow returned no token")
	}
	if got := counts(t, ts)["/v1/cli/device/poll"]; got != 3 {
		t.Errorf("poll count = %d, want 3 (two pending, one authorized)", got)
	}
}

func TestOnboardingWithoutABearerTokenIs401(t *testing.T) {
	ts, _ := newTestServer(t, Options{})
	resp := do(t, "GET", ts.URL+"/v1/cli/onboarding", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

func TestEnrollRejectsAnEmptyCode(t *testing.T) {
	ts, _ := newTestServer(t, Options{})
	resp := do(t, "POST", ts.URL+"/v1/cli/enroll", `{"code":""}`,
		map[string]string{"Content-Type": "application/json"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for an empty setup code", resp.StatusCode)
	}
}

// ---- the publish routes ----

func ingestRoutes() []string {
	return []string{
		"/v1/enrichments",
		"/v1/signal/blocks",
		"/v1/signal/features",
		"/v1/signal/client-events",
		"/v1/logs",
		"/v1/metrics",
	}
}

func TestEveryPublishRouteCountsAndPersistsItsBody(t *testing.T) {
	state := t.TempDir()
	ts, _ := newTestServer(t, Options{StateDir: state})

	for _, path := range ingestRoutes() {
		resp := do(t, "POST", ts.URL+path, `{"hello":"world"}`,
			map[string]string{"x-keld-ingest-token": DefaultIngestToken})
		if resp.StatusCode >= 400 {
			t.Errorf("POST %s = %d, want < 400", path, resp.StatusCode)
		}
		// ⚠️ The client treats a 2xx whose body does not start with { or [ as a
		// captive portal and retries forever. Empty or JSON only.
		body, _ := io.ReadAll(resp.Body)
		if b := strings.TrimSpace(string(body)); b != "" && b[0] != '{' && b[0] != '[' {
			t.Errorf("POST %s answered %q — the client reads that as a captive portal", path, b)
		}
	}

	got := counts(t, ts)
	for _, path := range ingestRoutes() {
		if got[path] != 1 {
			t.Errorf("count[%s] = %d, want 1", path, got[path])
		}
	}

	// One file per received body, under a directory named for the route.
	for _, path := range ingestRoutes() {
		dir := filepath.Join(state, slug(path))
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("no state dir for %s: %v", path, err)
		}
		if len(entries) != 1 {
			t.Fatalf("%s holds %d files, want 1", dir, len(entries))
		}
		raw, err := os.ReadFile(filepath.Join(dir, entries[0].Name()))
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if strings.TrimSpace(string(raw)) != `{"hello":"world"}` {
			t.Errorf("%s body = %q, want the posted body verbatim", path, raw)
		}
	}
}

func TestPublishWithoutAnIngestTokenIs401(t *testing.T) {
	ts, _ := newTestServer(t, Options{})
	for _, path := range ingestRoutes() {
		resp := do(t, "POST", ts.URL+path, `{}`, nil)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("POST %s with no token = %d, want 401", path, resp.StatusCode)
		}
	}
	// A rejected request is not counted as a delivery, or the checkpoint that
	// reads "telemetry forwarded" would pass on 401s.
	for path, n := range counts(t, ts) {
		if strings.HasPrefix(path, "/v1/") && n != 0 {
			t.Errorf("count[%s] = %d after a 401, want 0", path, n)
		}
	}
}

func TestPublishAlsoAcceptsTheQueryTokenAndTheTelemetrySecretHeader(t *testing.T) {
	// The three shapes teleproxy accepts exist because the TOOLS disagree; the
	// daemon itself only ever sends the header, but a test that pins one shape
	// is how the live 401 happened before. Accept all three.
	ts, _ := newTestServer(t, Options{})
	resp := do(t, "POST", ts.URL+"/v1/logs?token="+DefaultIngestToken, `{}`, nil)
	if resp.StatusCode >= 400 {
		t.Errorf("query-token POST = %d, want < 400", resp.StatusCode)
	}
	resp = do(t, "POST", ts.URL+"/v1/metrics", `{}`,
		map[string]string{"x-keld-telemetry-secret": DefaultIngestToken})
	if resp.StatusCode >= 400 {
		t.Errorf("telemetry-secret POST = %d, want < 400", resp.StatusCode)
	}
}

func TestEnrichmentSettingsReturnsAnEmptyJSONObject(t *testing.T) {
	ts, _ := newTestServer(t, Options{})
	resp := do(t, "GET", ts.URL+"/v1/enrichment-settings", "",
		map[string]string{"x-keld-ingest-token": DefaultIngestToken})
	// ⚠️ The settings client demands EXACTLY 200 and a decodable JSON body; an
	// empty body is a decode error there, unlike on the publish routes.
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want exactly 200", resp.StatusCode)
	}
	var m map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("body is not a JSON object: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("body = %v, want {} so no remote override is asserted", m)
	}
}

func TestCountsSurviveAndAccumulate(t *testing.T) {
	ts, _ := newTestServer(t, Options{})
	h := map[string]string{"x-keld-ingest-token": DefaultIngestToken}
	for i := 0; i < 3; i++ {
		do(t, "POST", ts.URL+"/v1/logs", `{}`, h)
	}
	if got := counts(t, ts)["/v1/logs"]; got != 3 {
		t.Errorf("count = %d, want 3", got)
	}
}

func TestUnknownRouteIs404AndCounted(t *testing.T) {
	ts, _ := newTestServer(t, Options{})
	resp := do(t, "POST", ts.URL+"/v1/unknown", `{}`,
		map[string]string{"x-keld-ingest-token": DefaultIngestToken})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
	if got := counts(t, ts)["/v1/unknown"]; got != 1 {
		t.Errorf("count[/v1/unknown] = %d, want 1 — an unserved route the client "+
			"reaches for is the first thing to look at on a failure", got)
	}
}
