package usage

import (
	"bufio"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

type memSink struct {
	mu    sync.Mutex
	calls int
	rows  map[string]ledger.RequestRow
}

func (m *memSink) InsertRequests(rows []ledger.RequestRow) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.rows == nil {
		m.rows = map[string]ledger.RequestRow{}
	}
	n := 0
	for _, r := range rows {
		k := r.Source + "|" + r.Session + "|" + r.Key
		if _, ok := m.rows[k]; !ok {
			m.rows[k] = r
			n++
		}
	}
	return n
}

func fixture(name string) string { return filepath.Join("..", "promptlog", "testdata", name) }

func feed(t *testing.T, r *Recorder, source, name string) {
	t.Helper()
	f, err := os.Open(fixture(name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		r.Observe(source, fixture(name), append([]byte(nil), sc.Bytes()...))
	}
}

func flatPrice(model string, in, out, cr, cc int64) (float64, bool) {
	if model == "" {
		return 0, false
	}
	return float64(in+out+cr+cc) / 1e6, true
}

// One poll's records are written in ONE insert, priced at write.
func TestRecorderBuffersAndPricesAtWrite(t *testing.T) {
	sink := &memSink{}
	r := New(sink, flatPrice)
	feed(t, r, "claude_code", "claude_code_session.jsonl")
	feed(t, r, "codex", "codex_rollout.jsonl")
	r.ObserveFile("gemini_cli", fixture("gemini_session.json"))
	if sink.calls != 0 {
		t.Fatalf("wrote %d times before a flush, want 0", sink.calls)
	}
	if n := r.Flush(); n != 2+4+4 {
		t.Fatalf("flush added %d, want 10 (2 Claude Code, 4 Codex, 4 Gemini)", n)
	}
	if sink.calls != 1 {
		t.Fatalf("one flush made %d inserts, want 1", sink.calls)
	}
	for _, row := range sink.rows {
		if row.At.IsZero() {
			t.Fatalf("row with no instant: %+v", row)
		}
		want, _ := flatPrice(row.Model, row.Input, row.Output, row.CacheRead, row.CacheCreation)
		if row.EstimateUSD != want {
			t.Fatalf("row %s priced %v, want %v", row.Key, row.EstimateUSD, want)
		}
	}
	if n := r.Flush(); n != 0 || sink.calls != 1 {
		t.Fatalf("an empty flush wrote (n=%d, calls=%d)", n, sink.calls)
	}
}

// A model with no rate is still counted, at $0, for RepriceUnpricedRequests to
// fill in later.
func TestRecorderKeepsUnpricedRequests(t *testing.T) {
	sink := &memSink{}
	r := New(sink, func(string, int64, int64, int64, int64) (float64, bool) { return 0, false })
	feed(t, r, "claude_code", "claude_code_session.jsonl")
	if n := r.Flush(); n != 2 {
		t.Fatalf("added %d, want 2", n)
	}
	for _, row := range sink.rows {
		if row.EstimateUSD != 0 || row.Model == "" {
			t.Fatalf("got %+v", row)
		}
	}
}

// The buffer is bounded: a burst larger than the cap flushes itself.
func TestRecorderFlushesAtItsCap(t *testing.T) {
	sink := &memSink{}
	r := New(sink, flatPrice)
	r.cap = 3
	feed(t, r, "codex", "codex_rollout.jsonl") // 4 requests
	if sink.calls != 1 || len(sink.rows) != 3 {
		t.Fatalf("calls=%d rows=%d, want one self-flush of 3", sink.calls, len(sink.rows))
	}
	r.Flush()
	if len(sink.rows) != 4 {
		t.Fatalf("rows=%d after the final flush, want 4", len(sink.rows))
	}
}

// Sources the reader does not know yield nothing rather than a guessed row.
func TestRecorderIgnoresUnknownSources(t *testing.T) {
	sink := &memSink{}
	r := New(sink, flatPrice)
	feed(t, r, "chatgpt", "claude_code_session.jsonl")
	if n := r.Flush(); n != 0 {
		t.Fatalf("added %d for an unknown source", n)
	}
}
