package teleproxy

import (
	"bytes"
	"compress/gzip"
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

// ⚠️ FAIL CLOSED. subtle.ConstantTimeCompare("", "") returns 1, so a proxy built
// with no secret would authenticate every local caller — a fail-open default on
// the one route that injects billable usage into the org.
func TestAnEmptySecretRejectsEverything(t *testing.T) {
	p := New("http://a/v1/logs", "http://a/v1/metrics", func() string { return "t" }, "", t.TempDir())
	for _, sent := range []string{"", "anything"} {
		if rr := post(t, p, "/v1/logs", sent, `{"resourceLogs":[]}`); rr.Code != http.StatusUnauthorized {
			t.Fatalf("empty configured secret accepted %q: code = %d, want 401", sent, rr.Code)
		}
	}
}

// The record must be readable with the daemon STOPPED — that is the whole reason
// it goes to disk rather than living in memory. A fact only reachable from a
// running daemon would make daemon-down look like telemetry-broken, which is the
// confusion this check exists to end.
func TestLastForwardIsReadableFromDisk(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if _, known := LastForwardOnDisk(); known {
		t.Fatal("an absent record reported as known — that would be a finding on a machine with no proxy")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	p := New(srv.URL, srv.URL, func() string { return "t" }, "s", t.TempDir())
	post(t, p, "/v1/logs", "s", `{"resourceLogs":[]}`)
	p.WaitIdle()

	got, known := LastForwardOnDisk()
	if !known {
		t.Fatal("a successful forward left no on-disk record")
	}
	if got.IsZero() {
		t.Fatal("recorded a zero instant")
	}
}

// ⚠️ THE TOOLS DO NOT SEND OUR HEADER NAME, AND EVERY UNIT TEST HERE MISSED IT.
// Live, all three tools were 401'd by this proxy while the whole suite passed —
// because the suite used the name this package chose rather than the names the
// tool writers actually emit:
//
//	Claude Code / Codex   x-keld-ingest-token: <secret>
//	Gemini                ?token=<secret>          (its OTLP SDK sends no custom header)
//
// So the credential is accepted from every place a tool can put it. Changing the
// writers instead was the alternative and is worse: Gemini CANNOT send a custom
// header, so the query form has to be understood regardless, and the header
// shapes are the ones already known to work with each tool.
func TestAcceptsTheCredentialInEveryShapeAToolCanSendIt(t *testing.T) {
	const secret = "s3cret"
	p := New("http://a/v1/logs", "http://a/v1/metrics", func() string { return "t" }, secret, t.TempDir())

	// Claude Code and Codex: the ingest-token header.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader(`{"resourceLogs":[]}`))
	req.Header.Set("x-keld-ingest-token", secret)
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Errorf("x-keld-ingest-token header: code = %d, want 202 (Claude Code and Codex send this)", rr.Code)
	}

	// Gemini: the query parameter.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/logs?token="+secret, strings.NewReader(`{"resourceLogs":[]}`))
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Errorf("?token= query: code = %d, want 202 (Gemini's SDK sends no custom header)", rr.Code)
	}

	// And our own name keeps working.
	if rr := post(t, p, "/v1/logs", secret, `{"resourceLogs":[]}`); rr.Code != http.StatusAccepted {
		t.Errorf("x-keld-telemetry-secret: code = %d, want 202", rr.Code)
	}
}

// Widening where the credential may appear must not widen WHAT is accepted.
func TestAWrongCredentialIsRejectedInEveryShape(t *testing.T) {
	p := New("http://a/v1/logs", "http://a/v1/metrics", func() string { return "t" }, "right", t.TempDir())

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader(`{}`))
	req.Header.Set("x-keld-ingest-token", "wrong")
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("wrong ingest-token header accepted: %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/v1/logs?token=wrong", strings.NewReader(`{}`))
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("wrong ?token= accepted: %d", rr.Code)
	}
}

// ⚠️ THE DETECTOR MUST BE ARMED BEFORE THE FIRST FORWARD, NOT BY IT.
//
// LastForwardOnDisk reported known=false whenever the state file was absent, and
// the file was written only by a SUCCESSFUL forward — so the one population the
// doctor check exists for (a machine that migrated but whose tools were never
// restarted, and has therefore never forwarded) could never produce a finding.
// Verified live before this fix: credential three hours old, no forward ever,
// doctor silent. The unit test that "covered" it hand-built Known:true with a
// zero LastForward — a state the wiring could not produce.
//
// `known` now means "a proxy runs on this machine", which is what its doc always
// claimed, and is established at START.
func TestMarkRunningArmsTheDetectorBeforeAnyForward(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	if _, known := LastForwardOnDisk(); known {
		t.Fatal("known before the proxy has ever run")
	}
	if err := MarkRunning(); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	last, known := LastForwardOnDisk()
	if !known {
		t.Fatal("after MarkRunning the proxy must be KNOWN to run here")
	}
	if !last.IsZero() {
		t.Fatalf("last forward = %v, want zero — nothing has been forwarded yet", last)
	}
}

// MarkRunning must not erase a real forward when the daemon restarts.
func TestMarkRunningPreservesAnExistingForward(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := New(srv.URL, srv.URL, func() string { return "t" }, "s", t.TempDir())
	post(t, p, "/v1/logs", "s", `{"resourceLogs":[]}`)
	p.WaitIdle()
	before, known := LastForwardOnDisk()
	if !known || before.IsZero() {
		t.Fatalf("no forward recorded: known=%v t=%v", known, before)
	}

	if err := MarkRunning(); err != nil { // daemon restart
		t.Fatal(err)
	}
	after, known := LastForwardOnDisk()
	if !known || !after.Equal(before) {
		t.Fatalf("a restart erased the recorded forward: %v -> %v", before, after)
	}
}

// The config key must move the port too, or the documented remedy for a
// collision is unreachable on an installed daemon — no service definition on any
// OS carries an environment block.
func TestPortHonoursTheConfigKeyNotJustTheEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	t.Setenv(EnvPort, "")
	if err := os.WriteFile(filepath.Join(home, "agent-config.json"),
		[]byte(`{"telemetry_port": 15123}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Port(); got != 15123 {
		t.Fatalf("Port() = %d, want 15123 from agent-config.json", got)
	}
	if Addr() != "127.0.0.1:15123" {
		t.Fatalf("Addr() = %q", Addr())
	}
}

// ⚠️ A COMPRESSED BATCH MUST NOT BE FORWARDED AS PLAIN JSON.
//
// The forwarder hardcodes Content-Type: application/json and sets nothing else,
// and the listener discarded every inbound header. All three tools are currently
// configured for uncompressed JSON — but OTEL_EXPORTER_OTLP_COMPRESSION=gzip is a
// one-line org change, and the result would be gzip bytes labelled application/
// json with no Content-Encoding. Atlas 400s, that classifies as REFUSED, and the
// batch is dropped: silent total telemetry loss, one setting away.
//
// Decoding it here is the right call rather than rejecting: the tool is
// configured correctly and it is our transport that cannot carry it.
func TestGzippedBatchIsDecodedNotForwardedAsPlainJSON(t *testing.T) {
	var got []byte
	var enc string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		enc = r.Header.Get("Content-Encoding")
		got, _ = io.ReadAll(r.Body)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	p := New(srv.URL, srv.URL, func() string { return "t" }, "s", t.TempDir())

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(`{"resourceLogs":[{"marker":"PLAINTEXT-AFTER-DECODE"}]}`)); err != nil {
		t.Fatal(err)
	}
	zw.Close()

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", bytes.NewReader(buf.Bytes()))
	req.Header.Set("x-keld-telemetry-secret", "s")
	req.Header.Set("Content-Encoding", "gzip")
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusAccepted {
		t.Fatalf("gzipped batch rejected at the listener: %d", rr.Code)
	}
	p.WaitIdle()

	if enc != "" {
		t.Errorf("forwarded with Content-Encoding %q; the body was decoded, so it must not claim to be encoded", enc)
	}
	if !bytes.Contains(got, []byte("PLAINTEXT-AFTER-DECODE")) {
		t.Fatalf("Atlas did not receive decodable JSON: %q", got)
	}
}

// An encoding we cannot decode must be REFUSED at the listener, loudly, rather
// than forwarded as if it were JSON — the tool sees an error it can report
// instead of telemetry vanishing.
func TestAnUnsupportedContentEncodingIsRefusedAtTheListener(t *testing.T) {
	p := New("http://a/v1/logs", "http://a/v1/metrics", func() string { return "t" }, "s", t.TempDir())
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/logs", strings.NewReader("whatever"))
	req.Header.Set("x-keld-telemetry-secret", "s")
	req.Header.Set("Content-Encoding", "br")
	p.Handler().ServeHTTP(rr, req)
	if rr.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("code = %d, want 415 for an encoding we cannot decode", rr.Code)
	}
}
