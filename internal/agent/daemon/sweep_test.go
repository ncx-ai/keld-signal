package daemon

// Tests for runSweep, the daemon's periodic spool-maintenance loop extracted
// from Run's former inline sweep goroutine (see daemon.go). Run requires
// live credentials before it ever reaches this code, which is exactly why
// none of it had coverage before the extraction — these tests drive
// runSweep directly, with millisecond intervals and a real spool rooted at
// a t.TempDir() KELD_HOME, and no login required.
//
// Every test that starts runSweep waits for it to actually return
// (cancel(); <-done) before the test function ends, rather than just
// deferring cancel() and moving on. t.Setenv's KELD_HOME restore is a
// t.Cleanup, which runs AFTER the test function's own defers — so a
// still-running runSweep goroutine could otherwise take one more tick after
// KELD_HOME has already been restored to the developer's real value,
// falling back to ~/.keld and draining (deleting) real spooled prompts into
// a queue nobody reads. Under a completeness SLO that's exactly the kind of
// silent data loss this whole feature exists to prevent — caused by a test,
// which would be worse than having no test at all.
//
// A second, unrelated cross-test gotcha: internal/spool's evictedCount is a
// single package-level atomic, never reset between tests in this binary.
// TestRunSweepEvictedFiresOnlyOnChange induces its eviction and reads
// spool.Evicted() BEFORE starting runSweep specifically so it can assert
// against that absolute value (valid because runSweep's local lastEvicted
// always starts at 0 — daemon.go's runSweep) rather than needing a delta or
// any cross-test bookkeeping.

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

// findCode returns the last event in evts with the given code (the
// coalescing target if the emitter merged repeats into one ring slot).
func findCode(evts []clientevents.Event, code string) (clientevents.Event, bool) {
	var found clientevents.Event
	ok := false
	for _, e := range evts {
		if e.Code == code {
			found = e
			ok = true
		}
	}
	return found, ok
}

// assertNotCoalesced fails the test if ev carries a "count" field, which the
// Emitter's ring only ever adds when it merged 2+ consecutive same-code
// events into one slot (emitter.go's insert). A bare countCode(...)==1 over
// a drained batch cannot by itself distinguish "fired once" from "fired
// twice back-to-back and coalesced" — this closes that gap.
func assertNotCoalesced(t *testing.T, ev clientevents.Event) {
	t.Helper()
	if c, has := ev.Fields["count"]; has {
		t.Fatalf("%s coalesced with a prior same-code event (Fields[count]=%v present) — cannot conclude it fired exactly once from the drained batch alone", ev.Code, c)
	}
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
// returns, so tests can assert prompt cancellation and — just as
// importantly — wait for the goroutine to actually stop before returning,
// rather than merely canceling its context and hoping.
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
	done := runSweepAsync(ctx, q, emitter, 5*time.Millisecond, 10*time.Second)
	defer func() { cancel(); <-done }()

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
//
// gaugeIv is set to 1200x sweepIv (not the production 10x) specifically so
// the "no gauge yet" check below is a PROVABLE bound rather than one that
// merely happens to hold when the test runs fast: 5 rounds at a 500ms
// per-round cap is 2.5s worst case, comfortably under one 6s gaugeIv even
// under heavy scheduler contention. (An earlier version of this test used a
// 100ms per-round cap against a 1.5s gaugeIv — reproducibly, under -race
// plus 20+ CPU-bound busy loops on a 20-core machine, that still hit the
// per-round timeout often enough to be a real flake, not just a
// theoretical one; these wider margins were sized empirically against that
// same contention, not guessed.) The elapsed-time guard around the
// zero-gauge check below is a second, independent safety net for the same
// reason — so a future edit to these constants can't quietly turn the
// bound incidental again without the test noticing.
func TestRunSweepCadencesAreIndependent(t *testing.T) {
	sweepHome(t)

	q := queue.New(16)
	emitter := testEmitter()
	ctx, cancel := context.WithCancel(context.Background())

	const sweepIv = 5 * time.Millisecond
	const gaugeIv = 6 * time.Second // 1200x sweepIv
	done := runSweepAsync(ctx, q, emitter, sweepIv, gaugeIv)
	defer func() { cancel(); <-done }()

	// Write and drain several rows one at a time, each round-tripped well
	// inside a single gaugeIv window. If drains only happened on the gauge
	// cadence, these would never come back within their own timeout.
	const rounds = 5
	const perRoundTimeout = 500 * time.Millisecond // rounds*perRoundTimeout = 2.5s << gaugeIv (6s)
	start := time.Now()
	for i := 0; i < rounds; i++ {
		id := "R" + string(rune('0'+i))
		if err := spool.Write(ptr(id)); err != nil {
			t.Fatal(err)
		}
		j, ok := nextWithTimeout(q, perRoundTimeout)
		if !ok {
			t.Fatalf("round %d: drain did not pick up the new row within %v (<< gaugeIv %v)", i, perRoundTimeout, gaugeIv)
		}
		if j.ID != id {
			t.Fatalf("round %d: got job %q, want %q", i, j.ID, id)
		}
	}

	// By construction this always holds: nextWithTimeout's t.Fatalf halts
	// the test on any round slower than perRoundTimeout, so elapsed here is
	// bounded above by rounds*perRoundTimeout = 5*500ms = 2.5s, comfortably
	// under gaugeIv = 6s. The elapsed guard is a second, independent check
	// for the same invariant — belt and suspenders — in case that bound is
	// ever loosened without updating this comment.
	if elapsed := time.Since(start); elapsed < gaugeIv {
		early := emitter.Drain()
		if n := countCode(early, "spool.depth"); n != 0 {
			t.Fatalf("gauge fired %d times before its own interval elapsed (elapsed=%v, gaugeIv=%v; cadences re-coupled?): %v", n, elapsed, gaugeIv, codesOf(early))
		}
	} else {
		t.Logf("skipping the pre-gauge zero-check: %v already >= gaugeIv %v by the time the 5 rounds finished (this run was too slow to prove the negative — not evidence of re-coupling)", elapsed, gaugeIv)
	}

	// Now wait past gaugeIv and confirm exactly one gauge fires (and that
	// it wasn't a coalesced count>=2, i.e. genuinely one real tick, not
	// several folded into one ring slot).
	var seen []clientevents.Event
	waitFor(t, gaugeIv+4*time.Second, func() bool {
		seen = append(seen, emitter.Drain()...)
		return countCode(seen, "spool.depth") > 0
	})
	if n := countCode(seen, "spool.depth"); n != 1 {
		t.Fatalf("want exactly 1 spool.depth event, got %d: %v", n, codesOf(seen))
	}
	ev, _ := findCode(seen, "spool.depth")
	assertNotCoalesced(t, ev)
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
	// sweepIv never fires within this test's lifetime, so the drain can't
	// touch the seeded rows before the gauge does.
	done := runSweepAsync(ctx, q, emitter, 10*time.Second, 15*time.Millisecond)
	defer func() { cancel(); <-done }()

	var seen []clientevents.Event
	waitFor(t, 2*time.Second, func() bool {
		seen = append(seen, emitter.Drain()...)
		return countCode(seen, "spool.depth") > 0
	})

	ev, _ := findCode(seen, "spool.depth")
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
// The eviction is induced BEFORE runSweep is even started, and asserted
// against spool.Evicted()'s absolute value at that point (valid because
// runSweep's local lastEvicted always starts at 0). This is deliberate, not
// incidental: starting runSweep first and only then writing the
// budget-busting rows races runSweep's own drain against the writes — with
// an unconsumed queue (cap 16, no reader yet) every Offer succeeds, so
// spool.go's Drain deletes each row (and addBytes(-n)) about as fast as it
// arrives, and the running total can plausibly never reach the configured
// byte budget, so evictFor never trips at all. Inducing the eviction first,
// synchronously, with no daemon-managed reader running yet, removes that
// race entirely.
func TestRunSweepEvictedFiresOnlyOnChange(t *testing.T) {
	sweepHome(t)

	t.Setenv("KELD_SPOOL_MAX_BYTES", "3000")
	// Mirrors internal/spool's own TestByteBudgetEvictsOldestFirst: a
	// 3000-byte budget holds about 2 of these ~1000-byte records, so
	// writing 4 evicts some. spool.Write enforces the budget (and so
	// Evicted()) synchronously, so by the time this loop returns the
	// eviction has already happened — no sweep involved yet.
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
	want := spool.Evicted()
	if want <= 0 {
		t.Fatalf("setup did not evict anything: Evicted()=%d", want)
	}

	q := queue.New(16)
	emitter := testEmitter()
	ctx, cancel := context.WithCancel(context.Background())
	done := runSweepAsync(ctx, q, emitter, 5*time.Millisecond, 10*time.Second) // gaugeIv never fires
	defer func() { cancel(); <-done }()

	var seen []clientevents.Event
	waitFor(t, 2*time.Second, func() bool {
		seen = append(seen, emitter.Drain()...)
		return countCode(seen, "spool.evicted") > 0
	})
	if n := countCode(seen, "spool.evicted"); n != 1 {
		t.Fatalf("want exactly 1 spool.evicted event, got %d: %v", n, codesOf(seen))
	}
	ev, _ := findCode(seen, "spool.evicted")
	assertNotCoalesced(t, ev)
	dropped, ok := ev.Fields["dropped"].(int64)
	if !ok || dropped != want {
		t.Fatalf("dropped = %v (ok=%v), want %d (spool.Evicted() at fire time)", ev.Fields["dropped"], ok, want)
	}

	// Quiet sweeps: no further writes, no further evictions. This is the
	// real pin for "fires only on change" — confirm no repeat firing.
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
// negative (drainEnrichSpool's delete-time addBytes(-freed) would subtract
// bytes for a row the total never knew it had): Resync running correctly is
// what leaves the total at exactly 0 once every row (the tracked seed row
// and the untracked external one) has been drained.
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
	done := runSweepAsync(ctx, q, emitter, 5*time.Millisecond, 10*time.Second)
	defer func() { cancel(); <-done }()

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
	// The property is "returns rather than blocking forever on a ticker" — the bug
	// this guards is a hang, not a millisecond budget. The deadline is therefore
	// generous. At 200ms it flaked on loaded CI runners: commit 8cc5046 both PASSED
	// and FAILED on two runners of the same SHA, which blocks merges and costs
	// releases for nothing. A real hang still fails here in seconds rather than
	// waiting out the package timeout, so the diagnostic value is unchanged.
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("runSweep did not return after ctx cancellation — blocked on a ticker?")
	}
}
