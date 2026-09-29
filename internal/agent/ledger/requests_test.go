package ledger

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The per-request table (docs/v3/contracts.md → `requests`). One row per model
// request, keyed on the tool's own id for it, kept for good.

func req(key string, at time.Time, model string) RequestRow {
	return RequestRow{
		Source: "claude_code", Session: "s1", Key: key, At: at, Model: model,
		Input: 10, Output: 20, CacheRead: 60, CacheCreation: 10,
	}
}

func allRequests(t *testing.T, s *Store) []RequestRow {
	t.Helper()
	rows, err := s.UsageRows(time.Time{}, time.Now().Add(24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// T4: the same records inserted twice change nothing.
func TestInsertRequestsTwiceChangesNothing(t *testing.T) {
	setHome(t)
	s := New()
	now := time.Now().Truncate(time.Millisecond)
	batch := []RequestRow{req("r1", now, "m"), req("r2", now.Add(time.Second), "m")}
	if n, _ := s.InsertRequests(batch); n != 2 {
		t.Fatalf("first insert added %d, want 2", n)
	}
	if n, _ := s.InsertRequests(batch); n != 0 {
		t.Fatalf("second insert added %d, want 0", n)
	}
	rows := allRequests(t, s)
	if len(rows) != 2 {
		t.Fatalf("table holds %d rows, want 2", len(rows))
	}
	if !rows[0].At.Equal(now) || rows[0].Input != 10 || rows[0].CacheRead != 60 {
		t.Fatalf("row did not round-trip: %+v", rows[0])
	}
}

// T6: a key first seen a year ago, read again today (a transcript re-read from
// its start), adds nothing — the identity is the tool's, not the instant we
// read it.
func TestYearOldKeyReinsertedAddsNothing(t *testing.T) {
	setHome(t)
	s := New()
	old := time.Now().AddDate(-1, 0, 0).Truncate(time.Millisecond)
	s.InsertRequests([]RequestRow{req("r1", old, "m")})
	again := req("r1", old, "m")
	again.Output = 999 // a re-read cannot rewrite history either
	if n, _ := s.InsertRequests([]RequestRow{again}); n != 0 {
		t.Fatalf("re-insert added %d, want 0", n)
	}
	rows := allRequests(t, s)
	if len(rows) != 1 || rows[0].Output != 20 {
		t.Fatalf("got %+v, want the original row untouched", rows)
	}
}

// Same key under a different session or source is a different request.
func TestKeyIsScopedBySourceAndSession(t *testing.T) {
	setHome(t)
	s := New()
	now := time.Now()
	a := req("r1", now, "m")
	b := a
	b.Session = "s2"
	c := a
	c.Source = "codex"
	if n, _ := s.InsertRequests([]RequestRow{a, b, c}); n != 3 {
		t.Fatalf("added %d, want 3", n)
	}
}

// T12: a model the price table learns later has its rows re-priced; rows that
// are priced, unknown, or unnamed are left alone.
func TestRepriceUnpricedRequests(t *testing.T) {
	setHome(t)
	s := New()
	now := time.Now()
	priced := req("p", now, "known")
	priced.EstimateUSD = 7
	s.InsertRequests([]RequestRow{req("k", now, "known"), req("u", now, "unknown"), req("n", now, ""), priced})
	if n := s.RepriceUnpricedRequests(priceAt(0.01)); n != 1 {
		t.Fatalf("repriced %d rows, want 1", n)
	}
	got := map[string]float64{}
	for _, r := range allRequests(t, s) {
		got[r.Key] = r.EstimateUSD
	}
	if got["k"] != 1.0 || got["u"] != 0 || got["n"] != 0 || got["p"] != 7 {
		t.Fatalf("estimates %v, want k=1 u=0 n=0 p=7", got)
	}
	if n := s.RepriceUnpricedRequests(priceAt(0.01)); n != 0 {
		t.Fatalf("second pass repriced %d, want 0", n)
	}
}

// T9: rows and totals survive the transcript they were read from being
// deleted — the table holds the numbers, not a pointer to the file.
func TestRowsSurviveTheTranscriptBeingDeleted(t *testing.T) {
	setHome(t)
	transcript := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := New()
	s.InsertRequests([]RequestRow{req("r1", time.Now(), "m")})
	if err := os.Remove(transcript); err != nil {
		t.Fatal(err)
	}
	s2 := New() // a later daemon run
	rows := allRequests(t, s2)
	if len(rows) != 1 || rows[0].Input+rows[0].Output+rows[0].CacheRead+rows[0].CacheCreation != 100 {
		t.Fatalf("after the transcript went: %+v", rows)
	}
}

// A range is [since, until): a row exactly at until belongs to the next range.
func TestUsageRowsRange(t *testing.T) {
	setHome(t)
	s := New()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	s.InsertRequests([]RequestRow{req("a", base.Add(-time.Minute), "m"), req("b", base, "m"), req("c", base.Add(time.Hour), "m")})
	rows, err := s.UsageRows(base, base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Key != "b" {
		t.Fatalf("got %+v, want only b", rows)
	}
}

// Identifiers are shape-checked like every other ledger write: a row whose
// session or key is prose is refused, and a model that fails its shape is kept
// as unnamed rather than stored.
func TestInsertRequestsRefusesBadIdentifiers(t *testing.T) {
	setHome(t)
	s := New()
	now := time.Now()
	badSession := req("r1", now, "m")
	badSession.Session = "this is a sentence"
	badKey := req("has space", now, "m")
	badSource := req("r2", now, "m")
	badSource.Source = "chatgpt"
	noTime := req("r3", time.Time{}, "m")
	badModel := req("r4", now, "../etc/passwd")
	if n, _ := s.InsertRequests([]RequestRow{badSession, badKey, badSource, noTime, badModel}); n != 1 {
		t.Fatalf("added %d, want 1 (only the bad-model row, unnamed)", n)
	}
	rows := allRequests(t, s)
	if len(rows) != 1 || rows[0].Key != "r4" || rows[0].Model != "" {
		t.Fatalf("got %+v", rows)
	}
}

// The backfill marker lives in the same file, so removing ledger.db resets it.
func TestBackfillMarkerLivesInTheLedger(t *testing.T) {
	setHome(t)
	s := New()
	if s.RequestsBackfillDone() {
		t.Fatal("fresh ledger claims the backfill is done")
	}
	s.MarkRequestsBackfillDone(time.Now())
	if !New().RequestsBackfillDone() {
		t.Fatal("marker did not persist")
	}
	if err := os.Remove(dbPath()); err != nil {
		t.Fatal(err)
	}
	if New().RequestsBackfillDone() {
		t.Fatal("marker survived its ledger being deleted")
	}
}

func TestRequestStats(t *testing.T) {
	setHome(t)
	s := New()
	now := time.Now()
	s.InsertRequests([]RequestRow{req("a", now, "m"), req("b", now, "m")})
	st, err := s.RequestStats()
	if err != nil {
		t.Fatal(err)
	}
	if st.Rows != 2 || st.Bytes <= 0 {
		t.Fatalf("stats %+v, want 2 rows and a positive size", st)
	}
}

// FirstRequestAt is where each source's data starts (D2: nothing is shown
// before it).
func TestFirstRequestAtPerSource(t *testing.T) {
	setHome(t)
	s := New()
	base := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	cx := req("x", base, "m")
	cx.Source = "codex"
	s.InsertRequests([]RequestRow{req("a", base.Add(48*time.Hour), "m"), req("b", base.Add(24*time.Hour), "m"), cx})
	got, err := s.FirstRequestAt()
	if err != nil {
		t.Fatal(err)
	}
	if !got["claude_code"].Equal(base.Add(24*time.Hour)) || !got["codex"].Equal(base) || len(got) != 2 {
		t.Fatalf("first-at %v", got)
	}
}

// UsageBuckets sums requests per 5-minute bucket, per source, transcript and
// model: exact for the page, since block edges and every timezone offset fall
// on 5-minute boundaries.
func TestUsageBucketsSumPerFiveMinutes(t *testing.T) {
	setHome(t)
	s := New()
	base := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	a := req("a", base.Add(10*time.Second), "m")
	a.Transcript = "t1"
	b := req("b", base.Add(4*time.Minute+59*time.Second), "m")
	b.Transcript = "t1"
	c := req("c", base.Add(5*time.Minute), "m") // next bucket
	c.Transcript = "t1"
	d := req("d", base.Add(time.Minute), "other")
	d.Transcript = "t1"
	e := req("e", base.Add(time.Minute), "m")
	e.Transcript = "agent-x" // a subagent's transcript, same session id
	for _, r := range []*RequestRow{&a, &b, &c, &d, &e} {
		r.EstimateUSD = 0.5
	}
	s.InsertRequests([]RequestRow{a, b, c, d, e})
	got, err := s.UsageBuckets(base, base.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	type k struct {
		at         int64
		transcript string
		model      string
	}
	m := map[k]UsageBucket{}
	for _, u := range got {
		m[k{u.At.Unix(), u.Transcript, u.Model}] = u
	}
	first := m[k{base.Unix(), "t1", "m"}]
	if len(got) != 4 || first.Requests != 2 || first.Input != 20 || first.CacheRead != 120 || first.EstimateUSD != 1.0 {
		t.Fatalf("buckets %+v", got)
	}
	if m[k{base.Add(5 * time.Minute).Unix(), "t1", "m"}].Requests != 1 ||
		m[k{base.Unix(), "t1", "other"}].Requests != 1 || m[k{base.Unix(), "agent-x", "m"}].Requests != 1 {
		t.Fatalf("buckets %+v", got)
	}
}
