package daemon

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/promptlog"
	"github.com/ncx-ai/keld-signal/internal/agent/usage"
	"github.com/ncx-ai/keld-signal/internal/agent/watch"
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

// A2: the backfill reads history into the table and sends none of it — even
// on a machine where every tool is mirrored and a token exists.
func TestBackfillSendsNothingToAtlas(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv, posts := countingAtlas(t)
	all := map[string]bool{"claude_code": true, "cowork": true, "codex": true, "gemini_cli": true, "gemini": true}
	tel := promptlog.New(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", func() string { return "tok" }, all)
	store := ledger.New()
	_, _ = transcriptObservers(tel, usage.New(store, priceStored)) // the live wiring exists beside it

	dir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join("..", "promptlog", "testdata", "codex_rollout.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "rollout-a.jsonl"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	bf := newUsageBackfill(store, func() []watch.Root { return []watch.Root{{SourceID: "codex", Dir: dir}} })
	for !bf.Step() {
	}
	if n := posts.Load(); n != 0 {
		t.Fatalf("backfill reached Atlas with %d bodies, want 0", n)
	}
	if got := len(requestRows(t, store)); got != 4 {
		t.Fatalf("backfill wrote %d requests, want 4", got)
	}
}

// T8: the local table and the Atlas mirror, fed by the same parse, sum to the
// same tokens — the mirror's records read the way Atlas reads them
// (services/codex.py and gemini.py subtract the cached prefix; thoughts bill
// as output).
func TestTableAndMirrorSumTheSame(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	var bodies []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/v1/logs") {
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			bodies = append(bodies, string(b))
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	all := map[string]bool{"claude_code": true, "codex": true, "gemini_cli": true}
	tel := promptlog.New(srv.URL+"/v1/logs", srv.URL+"/v1/metrics", func() string { return "tok" }, all)
	store := ledger.New()
	rec := usage.New(store, priceStored)
	line, doc := transcriptObservers(tel, rec)
	feedTranscript(t, line, "claude_code", "claude_code_session.jsonl")
	feedTranscript(t, line, "codex", "codex_rollout.jsonl")
	feedTranscript(t, line, "codex", "codex_reemission.jsonl")
	doc("gemini_cli", filepath.Join("..", "promptlog", "testdata", "gemini_session.json"))
	rec.Flush()

	type sums struct{ in, out, cr, cc, n int64 }
	mirror := map[string]*sums{}
	for _, body := range bodies {
		var p struct {
			ResourceLogs []struct {
				ScopeLogs []struct {
					LogRecords []struct {
						Attributes []struct {
							Key   string `json:"key"`
							Value struct {
								S string `json:"stringValue"`
								I string `json:"intValue"`
							} `json:"value"`
						} `json:"attributes"`
					} `json:"logRecords"`
				} `json:"scopeLogs"`
			} `json:"resourceLogs"`
		}
		if err := json.Unmarshal([]byte(body), &p); err != nil {
			t.Fatal(err)
		}
		for _, rl := range p.ResourceLogs {
			for _, sl := range rl.ScopeLogs {
				for _, lr := range sl.LogRecords {
					a := map[string]string{}
					for _, kv := range lr.Attributes {
						a[kv.Key] = kv.Value.S + kv.Value.I
					}
					n := func(k string) int64 { v, _ := strconv.ParseInt(a[k], 10, 64); return v }
					var src string
					var s sums
					switch a["event.name"] {
					case "api_request":
						src = "claude_code"
						s = sums{n("input_tokens"), n("output_tokens"), n("cache_read_tokens"), n("cache_creation_tokens"), 1}
					case "codex.sse_event":
						src = "codex"
						s = sums{n("input_tokens") - n("cached_tokens"), n("output_tokens"), n("cached_tokens"), n("cache_write_tokens"), 1}
					case "gemini_cli.api_response":
						src = "gemini_cli"
						s = sums{n("input_token_count") - n("cached_content_token_count"),
							n("output_token_count") + n("thoughts_token_count"), n("cached_content_token_count"), 0, 1}
					default:
						continue
					}
					m := mirror[src]
					if m == nil {
						m = &sums{}
						mirror[src] = m
					}
					m.in, m.out, m.cr, m.cc, m.n = m.in+s.in, m.out+s.out, m.cr+s.cr, m.cc+s.cc, m.n+s.n
				}
			}
		}
	}
	table := map[string]*sums{}
	for _, r := range requestRows(t, store) {
		m := table[r.Source]
		if m == nil {
			m = &sums{}
			table[r.Source] = m
		}
		m.in, m.out, m.cr, m.cc, m.n = m.in+r.Input, m.out+r.Output, m.cr+r.CacheRead, m.cc+r.CacheCreation, m.n+1
	}
	if len(table) != 3 || len(mirror) != 3 {
		t.Fatalf("sources: table %d, mirror %d, want all three in both", len(table), len(mirror))
	}
	for src, want := range mirror {
		if got := table[src]; got == nil || *got != *want {
			t.Fatalf("%s: table %+v, mirror %+v", src, got, want)
		}
	}
}
