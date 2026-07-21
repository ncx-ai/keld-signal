package promptlog

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestBuildLogsPayloadStructureNoText(t *testing.T) {
	ts := time.Unix(1700000000, 0)
	body, err := buildLogsPayload("cowork", "S1", "P1", ts)
	if err != nil {
		t.Fatal(err)
	}
	// Must not contain anything resembling prompt text — only ids/source/event.
	s := string(body)
	for _, banned := range []string{"content", "message", "text"} {
		if strings.Contains(s, banned) {
			t.Errorf("payload unexpectedly contains %q: %s", banned, s)
		}
	}
	var p otlpLogs
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.ResourceLogs) != 1 || len(p.ResourceLogs[0].ScopeLogs) != 1 || len(p.ResourceLogs[0].ScopeLogs[0].LogRecords) != 1 {
		t.Fatalf("unexpected OTLP shape: %+v", p)
	}
	attrs := map[string]string{}
	for _, a := range p.ResourceLogs[0].ScopeLogs[0].LogRecords[0].Attributes {
		attrs[a.Key] = a.Value.StringValue
	}
	if attrs["event.name"] != PromptEventName || attrs["session.id"] != "S1" || attrs["prompt.id"] != "P1" || attrs["source"] != "cowork" {
		t.Fatalf("log record attributes wrong: %+v", attrs)
	}
	res := map[string]string{}
	for _, a := range p.ResourceLogs[0].Resource.Attributes {
		res[a.Key] = a.Value.StringValue
	}
	if res["tool"] != "cowork" {
		t.Fatalf("resource tool attr wrong: %+v", res)
	}
	if p.ResourceLogs[0].ScopeLogs[0].LogRecords[0].TimeUnixNano != "1700000000000000000" {
		t.Fatalf("timeUnixNano wrong: %s", p.ResourceLogs[0].ScopeLogs[0].LogRecords[0].TimeUnixNano)
	}
}

func TestEmitPostsToEndpoint(t *testing.T) {
	type got struct {
		path, tokenHdr, actorHdr, body string
	}
	ch := make(chan got, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		ch <- got{r.URL.Path, r.Header.Get("x-keld-ingest-token"), r.Header.Get("x-keld-actor"), string(b)}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	e := New(srv.URL, func() string { return "tok-123" }, "admin@acme.test", map[string]bool{"cowork": true})
	e.Emit("cowork", "S1", "P1", time.Unix(1700000000, 0))

	select {
	case g := <-ch:
		if g.tokenHdr != "tok-123" || g.actorHdr != "admin@acme.test" {
			t.Fatalf("headers wrong: token=%q actor=%q", g.tokenHdr, g.actorHdr)
		}
		if !strings.Contains(g.body, "\"P1\"") || !strings.Contains(g.body, "\"cowork\"") {
			t.Fatalf("body missing ids: %s", g.body)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("emitter did not POST")
	}
}

func TestEmitSkipsIneligibleSourceAndNoToken(t *testing.T) {
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; w.WriteHeader(200) }))
	defer srv.Close()

	// claude_code not in the source set → no emit.
	e := New(srv.URL, func() string { return "tok" }, "a", map[string]bool{"cowork": true})
	e.Emit("claude_code", "S", "P", time.Now())
	// empty token → no emit.
	e2 := New(srv.URL, func() string { return "" }, "a", map[string]bool{"cowork": true})
	e2.Emit("cowork", "S", "P", time.Now())
	time.Sleep(200 * time.Millisecond)
	if hits != 0 {
		t.Fatalf("expected no POSTs, got %d", hits)
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
