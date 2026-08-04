package daemon

// Tests for runSweep, the daemon's periodic spool-maintenance loop extracted
// from Run's former inline sweep goroutine (see daemon.go). Run requires
// live credentials before it ever reaches this code, which is exactly why
// none of it had coverage before the extraction — these tests drive
// runSweep directly, with millisecond intervals and a real spool rooted at
// a t.TempDir() KELD_HOME, and no login required.
//
// One cross-test gotcha worth flagging up front: internal/spool's
// evictedCount is a single package-level atomic, not reset between tests in
// this binary. Tests that assert eviction behavior treat it as a
// monotonically increasing counter and work in deltas rather than absolute
// values, and drain any stale "catch-up" event before inducing the real
// eviction they want to assert on (see TestRunSweepEvictedFiresOnlyOnChange).

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

// sweepHome points the spool at a fresh temp KELD_HOME, mirroring
// internal/spool's own setHome test helper (the SQLite backend is keyed by
// dbPath(), so a fresh KELD_HOME gets a fresh *sql.DB and byte-total for
// free).
func sweepHome(t *testing.T) {
	t.Helper()
	t.Setenv("KELD_HOME", t.TempDir())
}

func ptr(id string) spool.Pointer {
	return spool.Pointer{
		Source:      spool.Source{ID: "claude_code", Origin: "hook"},
		Correlation: spool.Correlation{Scheme: "prompt_id", ID: id, SessionID: "S1"},
		Inline:      &spool.Inline{Text: "hello " + id},
	}
}

// codesOf extracts the Code of each event, preserving order.
func codesOf(evts []clientevents.Event) []string {
	var codes []string
	for _, e := range evts {
		codes = append(codes, e.Code)
	}
	return codes
}

func countCode(evts []clientevents.Event, code string) int {
	n := 0
	for _, e := range evts {
		if e.Code == code {
			n++
		}
	}
	return n
}

// nextWithTimeout reads one job from q, or reports ok=false if none arrives
// within timeout. Only used when a job is expected to already be in flight
// (or arrive shortly), since a timed-out call leaves its q.Next() goroutine
// blocked until a job eventually arrives — acceptable for these short-lived
// test processes.
func nextWithTimeout(q *queue.Queue, timeout time.Duration) (queue.Job, bool) {
	type res struct {
		job queue.Job
		ok  bool
	}
	ch := make(chan res, 1)
	go func() {
		j, ok := q.Next()
		ch <- res{j, ok}
	}()
	select {
	case r := <-ch:
		return r.job, r.ok
	case <-time.After(timeout):
		return queue.Job{}, false
	}
}

// runSweepAsync launches runSweep and returns a channel closed once it
// returns, so tests can assert prompt cancellation.
func runSweepAsync(ctx context.Context, q *queue.Queue, emitter *clientevents.Emitter, sweepIv, gaugeIv time.Duration) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		runSweep(ctx, q, emitter, sweepIv, gaugeIv)
		close(done)
	}()
	return done
}

// TestRunSweepDrainsSpooledRows pins the basic drain contract: rows written
// to the spool before runSweep starts are offered to q and removed from the
// spool within a sweep tick.
func TestRunSweepDrainsSpooledRows(t *testing.T) {
	sweepHome(t)
	for _, id := range []string{"P1", "P2", "P3"} {
		if err := spool.Write(ptr(id)); err != nil {
			t.Fatal(err)
		}
	}

	q := queue.New(16)
	emitter := testEmitter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runSweepAsync(ctx, q, emitter, 5*time.Millisecond, 10*time.Second)

	got := map[string]bool{}
	for i := 0; i < 3; i++ {
		j, ok := nextWithTimeout(q, 500*time.Millisecond)
		if !ok {
			t.Fatalf("timed out waiting for drained job #%d; got so far: %v", i+1, got)
		}
		got[j.ID] = true
	}
	for _, id := range []string{"P1", "P2", "P3"} {
		if !got[id] {
			t.Fatalf("expected drained job %q, got %v", id, got)
		}
	}

	waitFor(t, 500*time.Millisecond, func() bool {
		st, err := spool.Stats()
		return err == nil && st.Rows == 0
	})
}

// TestRunSweepCadencesAreIndependent pins the property Task 3's fix round
// established deliberately: the drain/resync/eviction-check cadence
// (sweepIv) and the spool.depth gauge cadence (gaugeIv) are two independent
// tickers, not one riding the other. With sweepIv far shorter than gaugeIv,
// several drains must complete before the first gauge tick — re-coupling
// them (e.g. "for finer visibility") would reintroduce the stale-gauge bug
// the emitter's same-code coalescing causes (see daemon.go's comment next to
// gaugeIv's computation).
func TestRunSweepCadencesAreIndependent(t *testing.T) {
	sweepHome(t)

	q := queue.New(16)
	emitter := testEmitter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const sweepIv = 5 * time.Millisecond
	const gaugeIv = 150 * time.Millisecond // 30x sweepIv
	runSweepAsync(ctx, q, emitter, sweepIv, gaugeIv)

	// Write and drain several rows one at a time, each round-tripped well
	// inside a single gaugeIv window. If drains only happened on the gauge
	// cadence, these would never come back within their own timeout.
	const rounds = 5
	for i := 0; i < rounds; i++ {
		id := "R" + string(rune('0'+i))
		if err := spool.Write(ptr(id)); err != nil {
			t.Fatal(err)
		}
		j, ok := nextWithTimeout(q, 60*time.Millisecond) // << gaugeIv
		if !ok {
			t.Fatalf("round %d: drain did not pick up the new row within %v (<< gaugeIv %v)", i, 60*time.Millisecond, gaugeIv)
		}
		if j.ID != id {
			t.Fatalf("round %d: got job %q, want %q", i, j.ID, id)
		}
	}

	// rounds*60ms of elapsed time is still comfortably under one gaugeIv
	// (150ms), so no gauge tick should have fired yet despite the several
	// drains that just ran.
	early := emitter.Drain()
	if n := countCode(early, "spool.depth"); n != 0 {
		t.Fatalf("gauge fired %d times before its own interval elapsed (cadences re-coupled?): %v", n, codesOf(early))
	}

	// Now wait past gaugeIv and confirm exactly one gauge fires.
	var seen []clientevents.Event
	waitFor(t, 2*time.Second, func() bool {
		seen = append(seen, emitter.Drain()...)
		return countCode(seen, "spool.depth") > 0
	})
	if n := countCode(seen, "spool.depth"); n != 1 {
		t.Fatalf("want exactly 1 spool.depth event, got %d: %v", n, codesOf(seen))
	}
}

// TestRunSweepGaugeReportsRealNumbers pins that the spool.depth event's
// rows/bytes/oldest_age_s reflect the spool's actual on-disk state, not
// merely that some event fired. sweepIv is set long enough that no drain
// ever runs, so the seeded rows are still in the spool, undisturbed, when
// the gauge fires.
func TestRunSweepGaugeReportsRealNumbers(t *testing.T) {
	sweepHome(t)
	for _, id := range []string{"G1", "G2", "G3"} {
		if err := spool.Write(ptr(id)); err != nil {
			t.Fatal(err)
		}
	}
	want, err := spool.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if want.Rows != 3 {
		t.Fatalf("setup: want 3 rows, got %d", want.Rows)
	}

	q := queue.New(16)
	emitter := testEmitter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// sweepIv never fires within this test's lifetime, so the drain can't
	// touch the seeded rows before the gauge does.
	runSweepAsync(ctx, q, emitter, 10*time.Second, 15*time.Millisecond)

	var seen []clientevents.Event
	waitFor(t, 2*time.Second, func() bool {
		seen = append(seen, emitter.Drain()...)
		return countCode(seen, "spool.depth") > 0
	})

	var ev clientevents.Event
	for _, e := range seen {
		if e.Code == "spool.depth" {
			ev = e
			break
		}
	}
	rows, ok := ev.Fields["rows"].(int64)
	if !ok || rows != want.Rows {
		t.Fatalf("rows = %v (ok=%v), want %d", ev.Fields["rows"], ok, want.Rows)
	}
	bytes, ok := ev.Fields["bytes"].(int64)
	if !ok || bytes != want.Bytes {
		t.Fatalf("bytes = %v (ok=%v), want %d", ev.Fields["bytes"], ok, want.Bytes)
	}
	oldestAgeS, ok := ev.Fields["oldest_age_s"].(float64)
	if !ok {
		t.Fatalf("oldest_age_s missing or wrong type: %v", ev.Fields["oldest_age_s"])
	}
	// oldest_age_s is derived from time.Since at the moment Stats() ran
	// inside the sweep, so it can't be asserted for exact equality against
	// a value computed here — just that it's a sane, small, non-negative
	// duration consistent with rows written moments ago in this same test.
	if oldestAgeS < 0 || oldestAgeS > 5 {
		t.Fatalf("oldest_age_s = %v, want a small non-negative duration", oldestAgeS)
	}
}

// TestRunSweepEvictedFiresOnlyOnChange pins that spool.evicted is a delta
// event: it fires once when Evicted() increases, and does not repeat on
// later quiet sweeps that observe no further change.
//
// internal/spool's evictedCount is a single process-global atomic (not
// reset between tests in this binary), so runSweep's local lastEvicted
// (which always starts at 0) can see a stale, already-nonzero Evicted()
// baseline on its very first tick and fire a one-off "catch-up" event for
// history that predates this test. That's a test-binary artifact, not a
// production concern (a real process's Evicted() starts at 0), so this test
// neutralizes it explicitly: let runSweep catch up and drain that event
// before inducing the real eviction it wants to assert on.
func TestRunSweepEvictedFiresOnlyOnChange(t *testing.T) {
	sweepHome(t)

	q := queue.New(16)
	emitter := testEmitter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const sweepIv = 5 * time.Millisecond
	const gaugeIv = 10 * time.Second // never fires
	runSweepAsync(ctx, q, emitter, sweepIv, gaugeIv)

	// Let any stale catch-up event settle, then discard it.
	time.Sleep(30 * time.Millisecond)
	emitter.Drain()

	before := spool.Evicted()
	// Mirrors internal/spool's own TestByteBudgetEvictsOldestFirst: a
	// 3000-byte budget holds about 2 of these ~1000-byte records, so
	// writing 4 evicts some.
	t.Setenv("KELD_SPOOL_MAX_BYTES", "3000")
	body := make([]byte, 1000)
	for i := range body {
		body[i] = 'x'
	}
	for _, id := range []string{"E1", "E2", "E3", "E4"} {
		p := ptr(id)
		p.Inline.Text = string(body)
		if err := spool.Write(p); err != nil {
			t.Fatal(err)
		}
	}
	wantDropped := spool.Evicted() - before
	if wantDropped <= 0 {
		t.Fatalf("setup did not evict anything: before=%d after=%d", before, spool.Evicted())
	}

	var seen []clientevents.Event
	waitFor(t, 2*time.Second, func() bool {
		seen = append(seen, emitter.Drain()...)
		return countCode(seen, "spool.evicted") > 0
	})
	if n := countCode(seen, "spool.evicted"); n != 1 {
		t.Fatalf("want exactly 1 spool.evicted event for this delta, got %d: %v", n, codesOf(seen))
	}
	var ev clientevents.Event
	for _, e := range seen {
		if e.Code == "spool.evicted" {
			ev = e
		}
	}
	dropped, ok := ev.Fields["dropped"].(int64)
	if !ok || dropped != wantDropped {
		t.Fatalf("dropped = %v (ok=%v), want %d", ev.Fields["dropped"], ok, wantDropped)
	}

	// Quiet sweeps: no further writes, no further evictions. Confirm no
	// repeat firing.
	time.Sleep(50 * time.Millisecond)
	quiet := emitter.Drain()
	if n := countCode(quiet, "spool.evicted"); n != 0 {
		t.Fatalf("spool.evicted repeated on a quiet sweep with no new eviction: %v", codesOf(quiet))
	}
}

// TestRunSweepResyncPicksUpExternalWrite pins the cross-process correction:
// the hook (cmd/keld) is a separate OS process writing to the same
// spool.db, so a row it inserts is invisible to the daemon's in-memory byte
// total until the next sweep's Resync call. This simulates that with a
// second, independent *sql.DB handle opened directly against the same
// spool.db file, inserting a row the same way spool.Write's upsert
// statement would — but bypassing spool's own addBytes bookkeeping
// entirely, exactly as an out-of-process writer would.
//
// If Resync did not run before the drain, the in-memory total would go
// negative (drainSpool's delete-time addBytes(-freed) would subtract bytes
// for a row the total never knew it had): Resync running correctly is what
// leaves the total at exactly 0 once every row (the tracked seed row and
// the untracked external one) has been drained.
func TestRunSweepResyncPicksUpExternalWrite(t *testing.T) {
	sweepHome(t)

	// Establish the daemon's own tracked handle + seed one row, so open()
	// seeds a byte total that does NOT yet know about the row inserted
	// below.
	if err := spool.Write(ptr("SEED")); err != nil {
		t.Fatal(err)
	}
	staleStats, err := spool.Stats()
	if err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(paths.SpoolDir(), "spool.db")
	raw, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("opening a second handle to %s: %v", dbPath, err)
	}
	defer raw.Close()
	externalBody := []byte(`{"source":{"id":"other-process"},"correlation":{"scheme":"prompt_id","id":"EXT"}}`)
	if _, err := raw.Exec(
		`INSERT INTO spool(source_id,corr_scheme,corr_id,bytes,body,ts) VALUES(?,?,?,?,?,?)`,
		"other-process", "prompt_id", "EXT", len(externalBody), externalBody, time.Now().UnixNano(),
	); err != nil {
		t.Fatalf("external insert: %v", err)
	}

	// The daemon's in-memory total is now stale: it does not yet include
	// the externally-inserted row's bytes, even though the table does.
	stillStale, err := spool.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if stillStale.Bytes != staleStats.Bytes {
		t.Fatalf("in-memory total should not have moved from the external insert alone: before=%d after=%d", staleStats.Bytes, stillStale.Bytes)
	}
	if stillStale.Rows != 2 {
		t.Fatalf("table should show both rows via COUNT(*): got %d", stillStale.Rows)
	}

	q := queue.New(16)
	emitter := testEmitter()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runSweepAsync(ctx, q, emitter, 5*time.Millisecond, 10*time.Second)

	// Drain both jobs off the queue so the sweep's drain can actually
	// delete both rows (Offer must succeed for Drain to remove a row).
	for i := 0; i < 2; i++ {
		if _, ok := nextWithTimeout(q, 500*time.Millisecond); !ok {
			t.Fatalf("timed out waiting for drained job #%d", i+1)
		}
	}

	waitFor(t, 500*time.Millisecond, func() bool {
		st, err := spool.Stats()
		return err == nil && st.Rows == 0
	})
	final, err := spool.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if final.Bytes != 0 {
		t.Fatalf("in-memory total should be exactly 0 once every row (tracked + externally-inserted) is drained — got %d. "+
			"A nonzero (in particular negative) total here means Resync did not fold in the external write before the drain's delete subtracted it",
			final.Bytes)
	}
}

// TestRunSweepCancellationStopsPromptly pins that ctx cancellation is
// noticed promptly (the ctx.Done() case in runSweep's select), regardless of
// which ticker last fired. It does not (and, given Go's tooling, cannot
// directly) assert that the tickers' underlying OS/runtime timers are
// reclaimed; it asserts the documented proxy for that — runSweep returns
// (running its two deferred Stop() calls) shortly after ctx is canceled,
// rather than lingering or blocking.
func TestRunSweepCancellationStopsPromptly(t *testing.T) {
	sweepHome(t)

	q := queue.New(16)
	emitter := testEmitter()
	ctx, cancel := context.WithCancel(context.Background())

	done := runSweepAsync(ctx, q, emitter, 5*time.Millisecond, 8*time.Millisecond)

	// Let several ticks of both cadences pass before canceling.
	time.Sleep(40 * time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("runSweep did not return promptly after ctx cancellation")
	}
}
