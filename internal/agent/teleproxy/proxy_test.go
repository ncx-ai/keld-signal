package teleproxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/retry"
)

// fast makes the proxy's transports give up quickly: these tests assert what
// happens AFTER the retries are exhausted, so the default backoff is pure delay.
func fast(p *Proxy) *Proxy {
	pol := retry.Policy{MaxAttempts: 1, BaseDelay: time.Millisecond, Multiplier: 1}
	p.logs.SetPolicy(pol)
	p.metric.SetPolicy(pol)
	return p
}

func post(t *testing.T, p *Proxy, path, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("x-keld-telemetry-secret", secret)
	p.Handler().ServeHTTP(rr, req)
	return rr
}

// This route injects billable usage into the org, and on a multi-user host any
// local user could forge it. That is why it authenticates at all, where the
// sidecar's loopback endpoints do not.
func TestRejectsWrongSecret(t *testing.T) {
	p := New("http://a/v1/logs", "http://a/v1/metrics", func() string { return "atlas" }, "s3cret", t.TempDir())
	if rr := post(t, p, "/v1/logs", "wrong", `{"resourceLogs":[]}`); rr.Code != http.StatusUnauthorized {
		t.Fatalf("code = %d, want 401", rr.Code)
	}
	if rr := post(t, p, "/v1/logs", "", `{"resourceLogs":[]}`); rr.Code != http.StatusUnauthorized {
		t.Fatalf("empty secret: code = %d, want 401", rr.Code)
	}
}

// ⚠️ The token is read PER REQUEST. Capturing it once would rebuild, inside the
// daemon, the exact stale-credential-in-memory bug this package exists to remove.
func TestForwardsWithTheCurrentAtlasToken(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("x-keld-ingest-token"))
		mu.Unlock()
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	tok := "first"
	p := New(srv.URL, srv.URL, func() string { mu.Lock(); defer mu.Unlock(); return tok }, "s3cret", t.TempDir())
	post(t, p, "/v1/logs", "s3cret", `{"resourceLogs":[]}`)
	p.WaitIdle()
	mu.Lock()
	tok = "rotated"
	mu.Unlock()
	post(t, p, "/v1/logs", "s3cret", `{"resourceLogs":[]}`)
	p.WaitIdle()

	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 || seen[0] != "first" || seen[1] != "rotated" {
		t.Fatalf("tokens seen = %v, want [first rotated]", seen)
	}
}

// The invariant enforced structurally rather than trusted from three tools'
// configuration defaults.
func TestPromptTextIsStrippedBeforeForwarding(t *testing.T) {
	var mu sync.Mutex
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		body, _ = io.ReadAll(r.Body)
		mu.Unlock()
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := New(srv.URL, srv.URL, func() string { return "t" }, "s", t.TempDir())
	in := `{"resourceLogs":[{"attributes":[` +
		`{"key":"user_prompt","value":{"stringValue":"MY SECRET PROMPT"}},` +
		`{"key":"gen_ai.completion","value":{"stringValue":"ALSO SECRET"}},` +
		`{"key":"model","value":{"stringValue":"claude-opus-5"}}]}]}`
	post(t, p, "/v1/logs", "s", in)
	p.WaitIdle()

	mu.Lock()
	defer mu.Unlock()
	if Contains(body, "MY SECRET PROMPT") || Contains(body, "ALSO SECRET") {
		t.Fatalf("prompt text was forwarded: %s", body)
	}
	if !Contains(body, "claude-opus-5") {
		t.Fatalf("stripping removed a legitimate attribute: %s", body)
	}
}

// A key no current tool emits must still be stripped: the guard is by shape, so
// it survives a tool inventing a new attribute name.
func TestStripsAnAttributeNameNoToolEmitsToday(t *testing.T) {
	out := StripText([]byte(`{"attributes":[{"key":"claude.user_prompt.raw","value":{"stringValue":"LEAK"}}]}`))
	if Contains(out, "LEAK") {
		t.Fatalf("an unfamiliar text key survived: %s", out)
	}
}

// A payload we cannot parse is passed through, not dropped: it is not ours to
// rewrite, and dropping would lose telemetry to a schema change.
func TestUnparseablePayloadPassesThrough(t *testing.T) {
	in := []byte(`not json at all`)
	if got := StripText(in); string(got) != string(in) {
		t.Fatalf("unparseable payload was altered: %s", got)
	}
}

// ⚠️ The tool must never be blocked by Atlas. Delivery is the spool's job.
func TestListenerAcceptsEvenWhenAtlasIsDown(t *testing.T) {
	dir := t.TempDir()
	p := fast(New("http://127.0.0.1:1/v1/logs", "http://127.0.0.1:1/v1/metrics",
		func() string { return "t" }, "s", dir))
	if rr := post(t, p, "/v1/logs", "s", `{"resourceLogs":[]}`); rr.Code != http.StatusAccepted {
		t.Fatalf("code = %d, want 202 — the tool must not wait on Atlas", rr.Code)
	}
	p.WaitIdle()
	files, _ := filepath.Glob(filepath.Join(dir, "logs", "*.json"))
	if len(files) == 0 {
		t.Fatal("nothing spooled — the batch was lost rather than kept")
	}
}

// Logs and metrics spool separately, so a poison metrics batch cannot block logs.
func TestLogsAndMetricsSpoolSeparately(t *testing.T) {
	dir := t.TempDir()
	p := fast(New("http://127.0.0.1:1/v1/logs", "http://127.0.0.1:1/v1/metrics",
		func() string { return "t" }, "s", dir))
	post(t, p, "/v1/metrics", "s", `{"resourceMetrics":[]}`)
	p.WaitIdle()
	if f, _ := filepath.Glob(filepath.Join(dir, "metrics", "*.json")); len(f) != 1 {
		t.Fatalf("metrics spool has %d files, want 1", len(f))
	}
	if f, _ := filepath.Glob(filepath.Join(dir, "logs", "*.json")); len(f) != 0 {
		t.Fatalf("a metrics batch landed in the logs spool (%d files)", len(f))
	}
}

// Three tools open at once all POST to this one port. Run with -race; the
// failure this catches is a shared buffer or an unsynchronised spool.
func TestConcurrentToolsDoNotCorruptTheSpool(t *testing.T) {
	dir := t.TempDir()
	p := fast(New("http://127.0.0.1:1/v1/logs", "http://127.0.0.1:1/v1/metrics",
		func() string { return "t" }, "s", dir))
	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			post(t, p, "/v1/logs", "s", fmt.Sprintf(`{"resourceLogs":[{"n":%d}]}`, i))
		}(i)
	}
	wg.Wait()
	p.WaitIdle()

	files, _ := filepath.Glob(filepath.Join(dir, "logs", "*.json"))
	if len(files) == 0 {
		t.Fatal("nothing spooled")
	}
	for _, f := range files {
		b, _ := os.ReadFile(f)
		var v any
		if json.Unmarshal(b, &v) != nil {
			t.Fatalf("spooled a corrupt body: %s", b)
		}
	}
}

// LastForward is what doctor reads to tell "flowing" from "silent".
func TestLastForwardAdvancesOnlyOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	down := fast(New("http://127.0.0.1:1/v1/logs", "http://127.0.0.1:1/v1/metrics",
		func() string { return "t" }, "s", t.TempDir()))
	post(t, down, "/v1/logs", "s", `{"resourceLogs":[]}`)
	down.WaitIdle()
	if !down.LastForward().IsZero() {
		t.Fatal("LastForward advanced despite Atlas being unreachable")
	}

	up := New(srv.URL, srv.URL, func() string { return "t" }, "s", t.TempDir())
	post(t, up, "/v1/logs", "s", `{"resourceLogs":[]}`)
	up.WaitIdle()
	if up.LastForward().IsZero() {
		t.Fatal("LastForward did not advance after a successful forward")
	}
}

func TestPortDefaultsTo14318AndIsOverridable(t *testing.T) {
	t.Setenv(EnvPort, "")
	if Port() != 14318 {
		t.Fatalf("default port = %d, want 14318 (deliberately not OTLP's 4317/4318)", Port())
	}
	t.Setenv(EnvPort, "15999")
	if Port() != 15999 {
		t.Fatalf("override ignored: %d", Port())
	}
	t.Setenv(EnvPort, "not-a-port")
	if Port() != 14318 {
		t.Fatalf("a junk override should fall back to the default, got %d", Port())
	}
}
