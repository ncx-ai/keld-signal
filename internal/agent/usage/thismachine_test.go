package usage

import (
	"os"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/watch"
)

// The backfill over THIS machine's real transcripts, into a throwaway ledger:
// how long it takes, how many requests it finds, and how many 5-minute buckets
// a month of real work makes. Reads ~/.claude, ~/.codex and ~/.gemini; writes
// only under a temp KELD_HOME. Opt-in:
//
//	KELD_USAGE_REAL=1 go test -run TestBackfillThisMachine -v -timeout 30m ./internal/agent/usage
func TestBackfillThisMachine(t *testing.T) {
	if os.Getenv("KELD_USAGE_REAL") != "1" {
		t.Skip("opt-in: set KELD_USAGE_REAL=1")
	}
	roots := watch.DiscoverRoots() // resolved against the real HOME, before KELD_HOME moves
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	b := NewBackfill(store, func(string, int64, int64, int64, int64) (float64, bool) { return 0, false },
		func() []watch.Root { return roots })
	b.PerStep = 1 << 30
	began := time.Now()
	b.Step()
	took := time.Since(began)
	st, err := store.RequestStats()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	month, err := store.UsageBuckets(now.AddDate(0, 0, -30), now)
	if err != nil {
		t.Fatal(err)
	}
	var reqs int64
	for _, u := range month {
		reqs += u.Requests
	}
	first, _ := store.FirstRequestAt()
	t.Logf("backfill: %d requests in %s · ledger %.1f MB live", st.Rows, took.Round(time.Second), float64(st.Bytes)/(1<<20))
	t.Logf("last 30 days: %d requests in %d five-minute buckets (%.1f per bucket)", reqs, len(month), float64(reqs)/float64(max(1, len(month))))
	for src, at := range first {
		t.Logf("  %s starts %s", src, at.Format("2006-01-02"))
	}
}
