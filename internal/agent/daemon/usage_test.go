package daemon

import (
	"bufio"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/promptlog"
	"github.com/ncx-ai/keld-signal/internal/agent/usage"
)

func countingAtlas(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func feedTranscript(t *testing.T, line func(string, string, []byte), source, fixture string) {
	t.Helper()
	path := filepath.Join("..", "promptlog", "testdata", fixture)
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line(source, path, append([]byte(nil), sc.Bytes()...))
	}
}

func requestRows(t *testing.T, l *ledger.Store) []ledger.RequestRow {
	t.Helper()
	rows, err := l.UsageRows(time.Time{}, time.Now().AddDate(10, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// R2: a tool that sends its own telemetry (tool_otlp on) is not mirrored — so
// Atlas does not count it twice — and is still counted locally.
func TestToolWithItsOwnTelemetryIsCountedLocallyAndNotMirrored(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_WATCH_TELEMETRY", "")
	t.Setenv("KELD_WATCH_TELEMETRY_SOURCES", "")
	srv, posts := countingAtlas(t)
	tel := promptlog.New(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", func() string { return "tok" },
		promptlog.SourcesFor(true))
	store := ledger.New()
	rec := usage.New(store, priceStored)
	line, doc := transcriptObservers(tel, rec)

	feedTranscript(t, line, "claude_code", "claude_code_session.jsonl")
	feedTranscript(t, line, "codex", "codex_rollout.jsonl")
	doc("gemini_cli", filepath.Join("..", "promptlog", "testdata", "gemini_session.json"))
	rec.Flush()

	if n := posts.Load(); n != 0 {
		t.Fatalf("mirror posted %d bodies for tools that send their own telemetry, want 0", n)
	}
	if got := len(requestRows(t, store)); got != 2+4+4 {
		t.Fatalf("counted %d requests locally, want 10", got)
	}
}

// An unpaired machine (no token) sends nothing and still counts.
func TestUnpairedMachineStillCounts(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv, posts := countingAtlas(t)
	tel := promptlog.New(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", func() string { return "" },
		promptlog.SourcesFor(false))
	store := ledger.New()
	rec := usage.New(store, priceStored)
	line, _ := transcriptObservers(tel, rec)

	feedTranscript(t, line, "claude_code", "claude_code_session.jsonl")
	rec.Flush()

	if n := posts.Load(); n != 0 {
		t.Fatalf("unpaired mirror posted %d bodies", n)
	}
	rows := requestRows(t, store)
	if len(rows) != 2 {
		t.Fatalf("counted %d, want 2", len(rows))
	}
	for _, r := range rows {
		if r.Model == "" || r.EstimateUSD <= 0 {
			t.Fatalf("row not named and priced: %+v", r)
		}
	}
}

// A mirrored tool is BOTH sent and counted, from the same parse.
func TestMirroredToolIsSentAndCounted(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv, posts := countingAtlas(t)
	tel := promptlog.New(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", func() string { return "tok" },
		map[string]bool{"codex": true})
	store := ledger.New()
	rec := usage.New(store, priceStored)
	line, _ := transcriptObservers(tel, rec)

	feedTranscript(t, line, "codex", "codex_rollout.jsonl")
	rec.Flush()

	if n := posts.Load(); n != 4 {
		t.Fatalf("mirror posted %d, want 4", n)
	}
	if got := len(requestRows(t, store)); got != 4 {
		t.Fatalf("counted %d, want 4", got)
	}
}
