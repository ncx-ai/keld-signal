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
	// Must be attributable to Cowork, not indistinguishable from CLI traffic:
	// a tool=<source> resource attribute (mirrors Cowork's native otelConfig).
	if !strings.Contains(body, `"tool"`) || !strings.Contains(body, `"cowork"`) {
		t.Fatalf("logs body missing tool=cowork marker: %s", body)
	}
}

func TestObserveAssistantEmitsApiRequestAndMetricsNoText(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"cowork": true})
	tp := coworkPath(t)
	line := `{"type":"assistant","sessionId":"S1","version":"2.1.216","timestamp":"2026-07-21T19:00:02Z","message":{"role":"assistant","model":"claude-opus-4-8","id":"req_123","content":[{"type":"text","text":"secret response body"}],"usage":{"input_tokens":10,"output_tokens":20,"cache_creation_input_tokens":5,"cache_read_input_tokens":3,"service_tier":"standard","cache_creation":{"ephemeral_1h_input_tokens":5,"ephemeral_5m_input_tokens":0}}}}`
	tel.Observe("cowork", tp, []byte(line))

	logs := c.bodies("/v1/logs")
	if len(logs) != 1 {
		t.Fatalf("expected 1 logs POST, got %d", len(logs))
	}
	lb := logs[0]
	if strings.Contains(lb, "secret response body") {
		t.Fatalf("response text leaked: %s", lb)
	}
	for _, want := range []string{"api_request", "claude-opus-4-8", "req_123", "input_tokens", "output_tokens", "service_tier", "cache_creation_1h_tokens"} {
		if !strings.Contains(lb, want) {
			t.Fatalf("logs body missing %q: %s", want, lb)
		}
	}
	// Cost is NOT emitted client-side (no first-hand cost; Atlas computes it).
	if strings.Contains(lb, "cost_usd") {
		t.Fatalf("api_request must not carry derived cost: %s", lb)
	}
	metrics := c.bodies("/v1/metrics")
	if len(metrics) != 1 {
		t.Fatalf("expected 1 metrics POST, got %d", len(metrics))
	}
	if !strings.Contains(metrics[0], "claude_code.token.usage") {
		t.Fatalf("metrics missing token.usage: %s", metrics[0])
	}
	if strings.Contains(metrics[0], "claude_code.cost.usage") {
		t.Fatalf("cost.usage metric must not be emitted (Atlas computes cost): %s", metrics[0])
	}
}

// TestFidelityMirrorsCLISchema enforces that each emitted log event carries
// exactly the CLI's captured attribute key set MINUS the documented omissions
// (prompt/response text = privacy; the rest = not reconstructable host-side). Any
// structural drift from the CLI schema fails here.
//
// ⚠️ **`assistant_response` IS NO LONGER MIRRORED, AND THAT IS A DELIBERATE
// REMOVAL RATHER THAN AN OVERSIGHT.** It carried exactly one field of its own,
// `response_length`, which is a MEASUREMENT OF RESPONSE TEXT and is priced by
// nothing. It also could not be told apart from `api_request` under the natural
// dedup key: Claude Code stamps both records with the SAME `request_id` and
// distinguishes them only by `event.sequence`, a process-local counter no
// transcript reproduces. And Claude Code writes one assistant LINE per content
// block (measured: 13,755 lines over 7,683 requests on the 40 largest real
// transcripts here), so a per-request `response_length` is not even computable
// without buffering a whole request. Dropping it removes a text-derived number
// from the wire and leaves one record per request. Re-adding it needs a key that
// is not the request id.
func TestFidelityMirrorsCLISchema(t *testing.T) {
	// Oracle: captured from a real `claude` OTLP export (2026-07-21).
	oracle := map[string][]string{
		"user_prompt": {"event.name", "event.sequence", "event.timestamp", "message.uuid", "organization.id", "prompt", "prompt.id", "prompt_length", "session.id", "terminal.type", "user.account_id", "user.account_uuid", "user.email", "user.id"},
		"api_request": {"cache_creation_tokens", "cache_read_tokens", "client_request_id", "cost_usd", "cost_usd_micros", "duration_ms", "effort", "event.name", "event.sequence", "event.timestamp", "input_tokens", "model", "organization.id", "output_tokens", "prompt.id", "query_source", "request_id", "session.id", "speed", "terminal.type", "user.account_id", "user.account_uuid", "user.email", "user.id"},
	}
	// Documented omissions: privacy (text) + not reconstructable host-side +
	// cost (dropped deliberately — no first-hand cost in the transcript; Atlas
	// computes it authoritatively from the exact tokens we emit).
	omit := map[string]bool{
		"prompt": true, "response": true, // privacy — never emit text
		"terminal.type": true, "user.id": true, "user.account_id": true, // no host-side source
		"duration_ms": true, "query_source": true, "speed": true, // runtime-only
		"cost_usd": true, "cost_usd_micros": true, // derived cost dropped; Atlas computes from tokens
	}
	// Per-EVENT omissions. ⚠️ `event.sequence` is dropped from `api_request` only
	// — the PRICED record — because Atlas keys a Claude-Code row on
	// `session.id:event.sequence` and falls back to `request_id`, and a
	// process-local counter cannot dedup a re-read while the tool's own
	// `requestId` can. `user_prompt` keeps it: it carries no priced field and
	// Claude Code sends no request id on it, so there is no natural key to use
	// instead and a duplicate costs a prompt count rather than money.
	omitFrom := map[string]map[string]bool{
		"api_request": {"event.sequence": true},
	}
	// Intentional extensions beyond the CLI schema: exact token detail Atlas needs
	// to compute cost accurately (service tier + 1h/5m cache-write split).
	extra := map[string]map[string]bool{
		"api_request": {"service_tier": true, "cache_creation_1h_tokens": true, "cache_creation_5m_tokens": true},
	}

	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"cowork": true})
	tp := coworkPath(t)
	// user first (establishes prompt.id linkage), then assistant.
	tel.Observe("cowork", tp, []byte(`{"type":"user","promptId":"P1","uuid":"U1","sessionId":"S1","version":"2.1.216","timestamp":"2026-07-21T19:00:00Z","message":{"role":"user","content":"hi"}}`))
	tel.Observe("cowork", tp, []byte(`{"type":"assistant","uuid":"AU1","parentUuid":"U1","requestId":"req_1","effort":"high","sessionId":"S1","version":"2.1.216","timestamp":"2026-07-21T19:00:02Z","message":{"role":"assistant","model":"claude-opus-4-8","id":"msg_1","content":[{"type":"text","text":"yo"}],"usage":{"input_tokens":2,"output_tokens":5,"cache_creation_input_tokens":7,"cache_read_input_tokens":9,"service_tier":"standard","cache_creation":{"ephemeral_1h_input_tokens":7,"ephemeral_5m_input_tokens":0}}}}`))

	got := emittedKeysByEvent(t, c.bodies("/v1/logs"))
	for event, keys := range oracle {
		want := map[string]bool{}
		for _, k := range keys {
			if !omit[k] && !omitFrom[event][k] {
				want[k] = true
			}
		}
		for k := range extra[event] {
			want[k] = true
		}
		have := got[event]
		if have == nil {
			t.Fatalf("event %q was never emitted", event)
		}
		for k := range want {
			if !have[k] {
				t.Errorf("%s: MISSING key %q (CLI has it, not omitted)", event, k)
			}
		}
		for k := range have {
			if !want[k] {
				t.Errorf("%s: EXTRA key %q not in CLI schema", event, k)
			}
		}
	}
}

// emittedKeysByEvent parses captured logs bodies → event.name → set of attr keys.
func emittedKeysByEvent(t *testing.T, bodies []string) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	for _, b := range bodies {
		var p otlpLogs
		if err := json.Unmarshal([]byte(b), &p); err != nil {
			t.Fatal(err)
		}
		for _, rl := range p.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					keys := map[string]bool{}
					var event string
					for _, a := range lr.Attributes {
						keys[a.Key] = true
						if a.Key == "event.name" {
							event = a.Value.StringValue
						}
					}
					out[event] = keys
				}
			}
		}
	}
	return out
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

// ⚠️ THESE TWO USED TO ASSERT THAT CODEX AND GEMINI "MUST NOT BE HOST-SIDE
// EMITTED; THEIR NATIVE OTEL IS USED", AND THE MECHANISM NOW EXISTS FOR BOTH.
// What they pin is narrower and still load-bearing: the ENV DEFAULT does not
// turn them on. Widening the mirror to three tools and deciding which tools it
// mirrors are separate changes — the policy is the per-tool `tool_otlp` setting,
// applied through SetSources at the one call site in daemon.go (WS3). A default
// flipped here would start mirroring on every machine the moment this binary
// shipped, beside tools still posting their own OTLP, and Atlas would count the
// same request twice for as long as both ran.
func TestCodexNotEnabledByTheEnvDefault(t *testing.T) {
	t.Setenv("KELD_WATCH_TELEMETRY", "")
	t.Setenv("KELD_WATCH_TELEMETRY_SOURCES", "")
	if SourcesFromEnv()["codex"] {
		t.Error("codex must not be in the default source set; WS3's tool_otlp switch turns it on")
	}
}

func TestGeminiNotEnabledByTheEnvDefault(t *testing.T) {
	t.Setenv("KELD_WATCH_TELEMETRY", "")
	t.Setenv("KELD_WATCH_TELEMETRY_SOURCES", "")
	if SourcesFromEnv()["gemini"] {
		t.Error("gemini must not be in the default source set; WS3's tool_otlp switch turns it on")
	}
}

// TestSetSourcesIsTheSwitch covers the seam WS3 wires: the set is replaceable at
// runtime, replacement is whole (never merged — a stale source left behind is a
// tool being mirrored after its own OTLP was turned back on), and a source that
// is off emits nothing at all.
func TestSetSourcesIsTheSwitch(t *testing.T) {
	c, srv := newCapSink()
	defer srv.Close()
	tel := telFor(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", map[string]bool{"cowork": true})

	tel.Observe("claude_code", "/tmp/s.jsonl", []byte(claudeAssistantLine))
	if n := len(c.bodies("/v1/logs")); n != 0 {
		t.Fatalf("claude_code is not in the set; expected 0 POSTs, got %d", n)
	}

	tel.SetSources(map[string]bool{"claude_code": true})
	tel.Observe("claude_code", "/tmp/s.jsonl", []byte(claudeAssistantLine))
	if n := len(c.bodies("/v1/logs")); n != 1 {
		t.Fatalf("after SetSources expected 1 POST, got %d", n)
	}
	// cowork must be GONE, not merged.
	tel.Observe("cowork", "/tmp/c.jsonl", []byte(claudeAssistantLine))
	if n := len(c.bodies("/v1/logs")); n != 1 {
		t.Fatalf("SetSources must REPLACE the set, not merge into it; got %d POSTs", n)
	}

	tel.SetSources(nil)
	tel.Observe("claude_code", "/tmp/s2.jsonl", []byte(claudeAssistantLine))
	if n := len(c.bodies("/v1/logs")); n != 1 {
		t.Fatalf("an empty set must mirror nothing; got %d POSTs", n)
	}
}

const claudeAssistantLine = `{"type":"assistant","requestId":"R9","uuid":"A9","sessionId":"S9","version":"2.1.274",` +
	`"timestamp":"2026-09-18T10:00:05.000Z","message":{"role":"assistant","model":"claude-opus-5","id":"msg_9",` +
	`"content":[],"usage":{"input_tokens":1,"output_tokens":2,"cache_read_input_tokens":3,"cache_creation_input_tokens":4}}}`

// sanity: buildLogsPayload helper removed; ensure JSON round-trips for a hand rec.
func TestLogRecordJSON(t *testing.T) {
	b, _ := json.Marshal(logRecord{Body: anyVal{StringValue: "x"}, Attributes: []kv{attr("a", "b")}})
	if !strings.Contains(string(b), `"stringValue":"x"`) {
		t.Fatalf("bad: %s", b)
	}
}
