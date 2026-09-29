package ledger

import (
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"
)

// T11: the requests table at scale. Rows are kept for good, so the question is
// what three years does to the file and to the page's reads.
//
// Opt-in (minutes, gigabytes of scratch):
//
//	KELD_USAGE_SCALE=1 go test -run TestRequestsTableAtScale -v -timeout 60m ./internal/agent/ledger
//
// The rate is this machine's, measured 2026-09-29: 25,339 distinct Claude Code
// requests in 30 days, ~845 a day. KELD_USAGE_SCALE_FACTOR scales it (default
// 10) and KELD_USAGE_SCALE_DAYS sets the span (default 1,095, three years).
// Row shapes are the real ones: a 36-char session uuid, a 28-char `req_…` key,
// the transcript's own 36-char name, a real model id.
func TestRequestsTableAtScale(t *testing.T) {
	if os.Getenv("KELD_USAGE_SCALE") != "1" {
		t.Skip("opt-in: set KELD_USAGE_SCALE=1")
	}
	factor := envInt("KELD_USAGE_SCALE_FACTOR", 10)
	days := envInt("KELD_USAGE_SCALE_DAYS", 1095)
	perDay := 845 * factor
	setHome(t)
	s := New()

	end := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)
	start := end.AddDate(0, 0, -days)
	models := []string{"claude-opus-5-5", "claude-sonnet-5", "claude-haiku-4-5-20251001", "gpt-5.5"}
	sources := []string{"claude_code", "claude_code", "claude_code", "codex"}
	began := time.Now()
	batch := make([]RequestRow, 0, 10_000)
	n := 0
	for d := 0; d < days; d++ {
		day := start.AddDate(0, 0, d)
		// ~6 sessions a day, the rest spread over an 8-hour working day.
		for i := 0; i < perDay; i++ {
			sess := fmt.Sprintf("%08x-7d4f-4c1a-9e2b-%012x", d, i%6)
			batch = append(batch, RequestRow{
				Source: sources[i%len(sources)], Session: sess, Transcript: sess,
				Key: fmt.Sprintf("req_011C%020d", n), At: day.Add(9*time.Hour + time.Duration(i)*8*time.Hour/time.Duration(perDay)),
				Model: models[i%len(models)], Input: 12, Output: 900, CacheRead: 81_000, CacheCreation: 4_000, EstimateUSD: 0.04,
			})
			n++
			if len(batch) == cap(batch) {
				s.InsertRequests(batch)
				batch = batch[:0]
			}
		}
	}
	s.InsertRequests(batch)
	wrote := time.Since(began)

	st, err := s.RequestStats()
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dbPath())
	if err != nil {
		t.Fatal(err)
	}

	timed := func(fn func()) time.Duration {
		t0 := time.Now()
		fn()
		return time.Since(t0)
	}
	var buckets []UsageBucket
	month := timed(func() {
		if buckets, err = s.UsageBuckets(end.AddDate(0, 0, -30), end); err != nil {
			t.Fatal(err)
		}
	})
	first := timed(func() {
		if _, err := s.FirstRequestAt(); err != nil {
			t.Fatal(err)
		}
	})
	// Every row is priced, so this is the start with nothing newly priced — the
	// one that happens on every daemon start.
	reprice := timed(func() { s.RepriceUnpricedRequests(func(string, int64, int64, int64, int64) (float64, bool) { return 0, false }) })
	stats := timed(func() { _, _ = s.RequestStats() })

	t.Logf("%d rows (%d/day × %d days) written in %s", st.Rows, perDay, days, wrote.Round(time.Second))
	t.Logf("ledger.db %.1f MB on disk, %.1f MB live (%.0f bytes/row)", mb(fi.Size()), mb(st.Bytes), float64(st.Bytes)/float64(st.Rows))
	t.Logf("30-day UsageBuckets: %s for %d buckets · FirstRequestAt %s · startup reprice scan %s · RequestStats %s",
		month.Round(time.Millisecond), len(buckets), first.Round(time.Millisecond), reprice.Round(time.Millisecond), stats.Round(time.Millisecond))

	if int(st.Rows) != n {
		t.Fatalf("table holds %d rows, wrote %d", st.Rows, n)
	}
	// The bars the page depends on. A 30-day read is what the Overview makes on
	// every load, and FirstRequestAt rides the same request.
	if month > 2*time.Second || first > 100*time.Millisecond || reprice > 100*time.Millisecond {
		t.Fatalf("too slow at scale: month %s, first %s, startup reprice %s", month, first, reprice)
	}
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil && v > 0 {
		return v
	}
	return def
}

func mb(b int64) float64 { return float64(b) / (1 << 20) }
