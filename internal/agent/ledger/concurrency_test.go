package ledger

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// TestConcurrentWritesNoLoss drives 10,000 Cut() calls for 10,000 DISTINCT
// blocks from 4 goroutines with no external synchronization (Recorder methods
// must be "safe from any goroutine", per recorder.go), then checks every one
// of them landed and the resulting file hasn't ballooned. The Store itself
// serializes actual SQL execution onto one connection (SetMaxOpenConns(1)),
// so this exercises that serialization under real contention rather than
// asserting it exists.
func TestConcurrentWritesNoLoss(t *testing.T) {
	setHome(t)
	s := New()

	const total = 10000
	const workers = 4
	const perWorker = total / workers

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				n := int64(w*perWorker + i)
				k := BlockKey{Session: fmt.Sprintf("session-%d", n), Start: n}
				s.Cut(k, n+60, "idle", "budget", "claude_code", time.Unix(n, 0))
			}
		}(w)
	}
	wg.Wait()

	snap, err := s.Read(time.Time{}, total+1)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Blocks) != total {
		t.Fatalf("want %d blocks, got %d — a write was lost under concurrency", total, len(snap.Blocks))
	}

	st, err := os.Stat(dbPath())
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	const bound = 50 << 20 // generous for 10k small rows; catches runaway growth, not fine-tunes size
	if st.Size() > bound {
		t.Fatalf("db file grew to %d bytes for %d rows (want < %d) — looks like unbounded growth", st.Size(), total, bound)
	}
}

// TestConcurrentUpdatesToSameKeysDoNotAccumulateRows is the harder half of
// the same property: many goroutines hammering a SMALL set of shared keys
// must upsert in place, never append. If ensureRow/setCell ever regressed to
// an INSERT instead of an upsert, this would show up as row count growing
// with write count instead of staying flat at the key count.
func TestConcurrentUpdatesToSameKeysDoNotAccumulateRows(t *testing.T) {
	setHome(t)
	s := New()

	const keys = 20
	const perWorkerIterations = 500 // 4 workers * 500 * 2 calls = 4,000 writes onto 20 rows
	const workers = 4

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perWorkerIterations; i++ {
				n := int64(i % keys)
				k := BlockKey{Session: fmt.Sprintf("shared-%d", n), Start: n}
				s.Cut(k, n+60, "idle", "budget", "claude_code", time.Now())
				s.Sent(k, time.Now())
			}
		}()
	}
	wg.Wait()

	snap, err := s.Read(time.Time{}, 1000)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(snap.Blocks) != keys {
		t.Fatalf("want exactly %d distinct blocks (upsert, not append), got %d", keys, len(snap.Blocks))
	}
	for _, b := range snap.Blocks {
		if _, ok := b.Cells["cut"]; !ok {
			t.Fatalf("block %v missing cut cell after concurrent writes", b.Key)
		}
		if _, ok := b.Cells["sent"]; !ok {
			t.Fatalf("block %v missing sent cell after concurrent writes", b.Key)
		}
	}
}
