package spool

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestByteBudgetEvictsOldestFirst(t *testing.T) {
	setHome(t)
	t.Setenv("KELD_SPOOL_MAX_BYTES", "3000")
	body := strings.Repeat("x", 1000)
	for _, id := range []string{"A", "B", "C", "D"} {
		if err := Write(inlinePtr(id, body)); err != nil {
			t.Fatal(err)
		}
	}
	var order []string
	Drain(func(p Pointer) error { order = append(order, p.Correlation.ID); return nil })
	// Each record is >1000 bytes with envelope, so a 3000-byte budget holds 2.
	if len(order) > 2 {
		t.Fatalf("budget not enforced: %v", order)
	}
	for _, id := range order {
		if id == "A" {
			t.Fatalf("oldest should have been evicted first, got %v", order)
		}
	}
	if Evicted() == 0 {
		t.Fatal("eviction count not reported — a silent drop is the failure mode we care about")
	}
}

func TestOversizedRecordIsRejectedNotInfiniteLooped(t *testing.T) {
	setHome(t)
	t.Setenv("KELD_SPOOL_MAX_BYTES", "500")
	err := Write(inlinePtr("BIG", strings.Repeat("x", 5000)))
	if err == nil {
		t.Fatal("a record larger than the whole budget must error, not evict everything forever")
	}
}

func TestBudgetDefaultAllowsRealBacklog(t *testing.T) {
	setHome(t)
	if got := maxBytes(); got < 256<<20 {
		t.Fatalf("default budget %d is too small for agent workloads; want >= 256 MB", got)
	}
}

// TestWriteCostDoesNotGrowWithDepth pins the property this whole plan exists to
// protect: evictFor must not run an O(N) full-table aggregate per write. A large
// but generous budget means eviction never actually triggers, so this isolates
// the per-write bookkeeping cost from eviction work itself. If evictFor regresses
// to a per-write `SELECT SUM(bytes) FROM spool`, the batch timed at depth ~5000
// takes meaningfully longer per row than the batch at depth ~500.
func TestWriteCostDoesNotGrowWithDepth(t *testing.T) {
	setHome(t)
	// A budget large enough that none of the ~5500 small records below ever
	// trigger eviction — this test is about per-write overhead, not eviction.
	t.Setenv("KELD_SPOOL_MAX_BYTES", "1000000000")

	writeBatch := func(prefix string, n int) time.Duration {
		start := time.Now()
		for i := 0; i < n; i++ {
			id := fmt.Sprintf("%s-%d", prefix, i)
			if err := Write(inlinePtr(id, "small body")); err != nil {
				t.Fatal(err)
			}
		}
		return time.Since(start)
	}

	// Warm the db/table up first so file growth/paging isn't counted in either
	// sample.
	writeBatch("warm", 500)

	early := writeBatch("early", 500) // depth ~500 -> ~1000
	// Pad depth up before the second timed batch.
	writeBatch("pad", 4000)         // depth ~1000 -> ~5000
	late := writeBatch("late", 500) // depth ~5000 -> ~5500

	t.Logf("early batch (depth ~1000): %v; late batch (depth ~5000): %v", early, late)
	if late > early*3 {
		t.Fatalf("per-write cost grew with depth: early=%v late=%v (>3x) — looks like a reintroduced O(N) scan per write", early, late)
	}
}
