package spool

import (
	"fmt"
	"strconv"
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

// TestSameIdentityRewriteDoesNotEvictUnrelatedRecords pins fix round 1's
// "Important" finding: evictFor must judge the eviction decision on the net
// byte delta a write causes, not the gross size of the new body. A
// same-identity rewrite (the ON CONFLICT DO UPDATE path in Write) is about to
// free the old row's bytes as part of the same upsert, so a same-size rewrite
// makes no net change to the table's total and must never evict an unrelated,
// still-queued record to make room for space that wasn't actually needed.
func TestSameIdentityRewriteDoesNotEvictUnrelatedRecords(t *testing.T) {
	setHome(t)
	body := strings.Repeat("x", 1000)

	// Measure this record's actual on-disk size (envelope + id vary the exact
	// byte count) so the budget below can be sized precisely around it, rather
	// than guessing a constant that might silently stop being tight enough.
	t.Setenv("KELD_SPOOL_MAX_BYTES", "1000000000")
	if err := Write(inlinePtr("A", body)); err != nil {
		t.Fatal(err)
	}
	db, err := open()
	if err != nil {
		t.Fatal(err)
	}
	var perRecord int64
	if err := db.QueryRow(`SELECT bytes FROM spool WHERE corr_id='A'`).Scan(&perRecord); err != nil {
		t.Fatal(err)
	}
	if _, err := Drain(func(Pointer) error { return nil }); err != nil {
		t.Fatal(err)
	}

	// A budget that holds exactly A and B (with a little slack) but not a
	// third record's worth of headroom.
	budget := perRecord*2 + 10
	t.Setenv("KELD_SPOOL_MAX_BYTES", strconv.FormatInt(budget, 10))

	if err := Write(inlinePtr("A", body)); err != nil {
		t.Fatal(err)
	}
	if err := Write(inlinePtr("B", body)); err != nil {
		t.Fatal(err)
	}
	before := Evicted()

	// Rewrite B with a body of identical length: the marshaled JSON — and so
	// the stored byte count — is identical, so the net delta is 0. This must
	// not evict A: a gross-size-based decision would see "need room for
	// perRecord more bytes" and evict the oldest (A) to get it, even though
	// the upsert is simultaneously freeing B's old perRecord bytes.
	if err := Write(inlinePtr("B", body)); err != nil {
		t.Fatal(err)
	}

	if Evicted() != before {
		t.Fatalf("same-size rewrite should not evict anything: evicted count went from %d to %d", before, Evicted())
	}
	var order []string
	Drain(func(p Pointer) error { order = append(order, p.Correlation.ID); return nil })
	if len(order) != 2 {
		t.Fatalf("expected both A and B to survive a same-size rewrite of B, got %v", order)
	}
}

// TestResyncPicksUpRowsFromAnotherWriter pins fix round 1's critical finding:
// the hook (cmd/keld) is a separate, short-lived OS process writing to the
// same spool.db, so from the long-lived daemon process's point of view a
// hook-inserted row is indistinguishable from this direct same-shape insert —
// bytes land in the table with no corresponding addBytes call on this
// process's in-memory total. Resync must re-learn the table's true total
// rather than leaving the daemon's counter permanently understating it (which
// would mean evictFor never trips and the spool grows unbounded).
func TestResyncPicksUpRowsFromAnotherWriter(t *testing.T) {
	setHome(t)
	if err := Write(inlinePtr("A", "hello")); err != nil {
		t.Fatal(err)
	}
	db, err := open()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := db.Exec(
		`INSERT INTO spool(source_id,corr_scheme,corr_id,bytes,body,ts) VALUES(?,?,?,?,?,?)`,
		"other-process", "prompt_id", "X", 500,
		[]byte(`{"source":{"id":"other-process"},"correlation":{"scheme":"prompt_id","id":"X"}}`),
		time.Now().UnixNano(),
	); err != nil {
		t.Fatal(err)
	}

	staleTotal := totalFor(db).Load()

	if err := Resync(); err != nil {
		t.Fatal(err)
	}

	resynced := totalFor(db).Load()
	if resynced == staleTotal {
		t.Fatalf("Resync should have picked up the externally-inserted row: stale=%d resynced=%d", staleTotal, resynced)
	}
	var want int64
	if err := db.QueryRow(`SELECT COALESCE(SUM(bytes),0) FROM spool`).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if resynced != want {
		t.Fatalf("resynced total = %d, want %d (table truth)", resynced, want)
	}
}
