package promptlog

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// capSink records POST bodies by path.
type capSink struct {
	mu sync.Mutex
	by map[string][]string
}

func newCapSink() (*capSink, *httptest.Server) {
	c := &capSink{by: map[string][]string{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		c.by[r.URL.Path] = append(c.by[r.URL.Path], string(b))
		c.mu.Unlock()
		w.WriteHeader(200)
	}))
	return c, srv
}
func (c *capSink) bodies(path string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.by[path]...)
}

// coworkPath builds a real-shaped cowork transcript path under home with identity
// metadata, and returns the transcript path.
func coworkPath(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	base := filepath.Join(home, "Library", "Application Support", "Claude", "local-agent-mode-sessions")
	acct, org, sess := "acct-9", "org-4", "local_s1"
	proj := filepath.Join(base, acct, org, sess, ".claude", "projects", "enc")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, acct, org, sess+".json"), []byte(`{"emailAddress":"dg@keld.co"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(proj, "sess.jsonl")
}

func telFor(logsURL, metricsURL string, sources map[string]bool) *Telemetry {
	return New(logsURL, metricsURL, func() string { return "tok" }, sources)
}

func TestObserveUserPromptEmitsIdentityNoText(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"cowork": true})
	tp := coworkPath(t)
	line := `{"type":"user","promptId":"P1","uuid":"U1","sessionId":"S1","version":"2.1.216","timestamp":"2026-07-21T19:00:00Z","message":{"role":"user","content":"secret prompt body"}}`
	tel.Observe("cowork", tp, []byte(line))

	logs := c.bodies("/v1/logs")
	if len(logs) != 1 {
		t.Fatalf("expected 1 logs POST, got %d", len(logs))
	}
	body := logs[0]
	if strings.Contains(body, "secret prompt body") {
		t.Fatalf("prompt text leaked into telemetry: %s", body)
	}
	for _, want := range []string{"user_prompt", `"P1"`, `"S1"`, "dg@keld.co", "org-4", "acct-9", "prompt_length", "service.name", "claude-code"} {
		if !strings.Contains(body, want) {
			t.Fatalf("logs body missing %q: %s", want, body)
		}
	}
}

func TestObserveAssistantEmitsApiRequestAndMetricsNoText(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"cowork": true})
	tp := coworkPath(t)
	line := `{"type":"assistant","sessionId":"S1","version":"2.1.216","timestamp":"2026-07-21T19:00:02Z","message":{"role":"assistant","model":"claude-opus-4-8","id":"req_123","content":[{"type":"text","text":"secret response body"}],"usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":5,"cache_read_input_tokens":3}}}`
	tel.Observe("cowork", tp, []byte(line))

	logs := c.bodies("/v1/logs")
	if len(logs) != 1 {
		t.Fatalf("expected 1 logs POST, got %d", len(logs))
	}
	lb := logs[0]
	if strings.Contains(lb, "secret response body") {
		t.Fatalf("response text leaked: %s", lb)
	}
	for _, want := range []string{"api_request", "assistant_response", "claude-opus-4-8", "req_123", "input_tokens", "output_tokens", "response_length"} {
		if !strings.Contains(lb, want) {
			t.Fatalf("logs body missing %q: %s", want, lb)
		}
	}
	metrics := c.bodies("/v1/metrics")
	if len(metrics) != 1 {
		t.Fatalf("expected 1 metrics POST, got %d", len(metrics))
	}
	if !strings.Contains(metrics[0], "claude_code.token.usage") {
		t.Fatalf("metrics missing token.usage: %s", metrics[0])
	}
	// cost.usage is derived for a known model.
	if !strings.Contains(metrics[0], "claude_code.cost.usage") {
		t.Fatalf("metrics missing cost.usage: %s", metrics[0])
	}
}

func TestObserveSkipsIneligibleAndEmptyToken(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tp := coworkPath(t)
	line := `{"type":"user","promptId":"P1","message":{"role":"user","content":"x"}}`
	// claude_code excluded by default source set.
	telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"cowork": true}).Observe("claude_code", tp, []byte(line))
	// empty token.
	New(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", func() string { return "" }, map[string]bool{"cowork": true}).Observe("cowork", tp, []byte(line))
	time.Sleep(150 * time.Millisecond)
	if n := len(c.bodies("/v1/logs")); n != 0 {
		t.Fatalf("expected no POSTs, got %d", n)
	}
}

func TestObserveIgnoresToolResultAndSidechain(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"cowork": true})
	tp := coworkPath(t)
	for _, line := range []string{
		`{"type":"user","promptId":"P","isSidechain":true,"message":{"role":"user","content":"sub"}}`,
		`{"type":"user","promptId":"P","toolUseResult":{"ok":true},"message":{"role":"user","content":"tr"}}`,
		`{"type":"user","promptId":"P","isMeta":true,"message":{"role":"user","content":"meta"}}`,
	} {
		tel.Observe("cowork", tp, []byte(line))
	}
	if n := len(c.bodies("/v1/logs")); n != 0 {
		t.Fatalf("synthetic user records must not emit; got %d", n)
	}
}

func TestSourcesFromEnv(t *testing.T) {
	t.Setenv("KELD_WATCH_TELEMETRY", "")
	t.Setenv("KELD_WATCH_TELEMETRY_SOURCES", "")
	if s := SourcesFromEnv(); !s["cowork"] || s["claude_code"] {
		t.Errorf("default should be {cowork}: %+v", s)
	}
	t.Setenv("KELD_WATCH_TELEMETRY", "off")
	if s := SourcesFromEnv(); len(s) != 0 {
		t.Errorf("off should disable: %+v", s)
	}
	t.Setenv("KELD_WATCH_TELEMETRY", "")
	t.Setenv("KELD_WATCH_TELEMETRY_SOURCES", "cowork,claude_code")
	if s := SourcesFromEnv(); !s["cowork"] || !s["claude_code"] {
		t.Errorf("override should include both: %+v", s)
	}
}

// sanity: buildLogsPayload helper removed; ensure JSON round-trips for a hand rec.
func TestLogRecordJSON(t *testing.T) {
	b, _ := json.Marshal(logRecord{Body: anyVal{StringValue: "x"}, Attributes: []kv{attr("a", "b")}})
	if !strings.Contains(string(b), `"stringValue":"x"`) {
		t.Fatalf("bad: %s", b)
	}
}
