package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/atlas"
	"github.com/ncx-ai/keld-signal/internal/retry"
)

// blockRow builds a minimal, valid publish.BlockEnrichment for these tests —
// enough for captureOnCut/blockKeyOf to key it and for a fake Atlas to see it
// arrive.
func blockRow(session string, startUnix int64) publish.BlockEnrichment {
	start := time.Unix(startUnix, 0).UTC().Format(time.RFC3339)
	end := time.Unix(startUnix+600, 0).UTC().Format(time.RFC3339)
	return publish.BlockEnrichment{
		SessionID: session,
		Source:    publish.Source{ID: "claude_code"},
		Window:    enrich.BlockRef{Start: start, End: end, SpanMinutes: 10},
	}
}

// fakeAtlasClient is a minimal atlas.Client for the republisher: it only
// needs SendBlocks and Enabled to matter here, but must implement the whole
// interface.
type fakeAtlasClient struct {
	mu       sync.Mutex
	enabled  bool
	sendErr  error
	status   int
	batches  [][]publish.BlockEnrichment
	sendCall int
	// failFirstN makes SendBlocks fail on the first N calls, then succeed —
	// used to exercise the backoff-then-recover path.
	failFirstN int
}

func (f *fakeAtlasClient) Enabled() bool { return f.enabled }

func (f *fakeAtlasClient) SendBlocks(_ context.Context, rows []publish.BlockEnrichment) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendCall++
	cp := append([]publish.BlockEnrichment(nil), rows...)
	f.batches = append(f.batches, cp)
	if f.sendErr != nil && f.sendCall <= f.failFirstN {
		return 0, f.sendErr
	}
	st := f.status
	if st == 0 {
		st = 200
	}
	return st, nil
}

func (f *fakeAtlasClient) Settings(context.Context) (settings.Remote, error) {
	return settings.Remote{}, nil
}
func (f *fakeAtlasClient) LastResponse() (int, time.Time) { return 0, time.Time{} }

var _ atlas.Client = (*fakeAtlasClient)(nil)

func TestCaptureOnCutStoresPayloadAndCallsNext(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	var nextCalled bool
	next := func(rows []publish.BlockEnrichment, path string) {
		nextCalled = true
		if len(rows) != 1 || path != "p" {
			t.Fatalf("next got unexpected args: %+v %q", rows, path)
		}
	}
	row := blockRow("sess-1", 1000)
	captureOnCut(store, next)([]publish.BlockEnrichment{row}, "p")

	if !nextCalled {
		t.Fatal("next must still be called")
	}
	got, err := store.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 captured payload, got %d", len(got))
	}
	var decoded publish.BlockEnrichment
	if err := json.Unmarshal(got[0].Payload, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.SessionID != "sess-1" {
		t.Fatalf("captured payload does not round-trip: %+v", decoded)
	}
}

func TestCaptureOnCutSkipsRowWithNoParsableWindow(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	bad := publish.BlockEnrichment{SessionID: "sess-2", Window: enrich.BlockRef{Start: "not-a-time"}}
	captureOnCut(store, nil)([]publish.BlockEnrichment{bad}, "p")

	got, err := store.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("an unparsable window must not be captured, got %+v", got)
	}
}

func TestCaptureOnCutIsolatesPanicInNext(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	panicky := func([]publish.BlockEnrichment, string) { panic("boom") }
	row := blockRow("sess-3", 2000)

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("captureOnCut must isolate a panic in next, got: %v", r)
			}
		}()
		captureOnCut(store, panicky)([]publish.BlockEnrichment{row}, "p")
	}()

	got, err := store.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatal("capture must still happen even though next panicked")
	}
}

func TestRepublishSweepNoOpWhenNothingCaptured(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	cl := &fakeAtlasClient{enabled: true}
	republishSweep(context.Background(), store, cl)
	if cl.sendCall != 0 {
		t.Fatalf("SendBlocks must not be called with nothing captured, got %d calls", cl.sendCall)
	}
}

func TestStartRepublisherNoopWhenAtlasOff(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	store.SaveUnsentPayload(ledger.BlockKey{Session: "s", Start: 1}, []byte(`{}`), time.Now())
	startRepublisher(context.Background(), store, atlas.Off{})
	// atlas.Off{} is disabled, so startRepublisher must not even start a
	// goroutine; give any accidental one a moment and confirm the payload is
	// untouched.
	time.Sleep(100 * time.Millisecond)
	got, err := store.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatal("a disabled client must never drain the captured table")
	}
}

func TestRepublishSweepPublishesInStartOrderAndClearsPayloads(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	now := time.Now()
	rows := []publish.BlockEnrichment{
		blockRow("sess-a", 3000),
		blockRow("sess-a", 1000),
		blockRow("sess-a", 2000),
	}
	for _, r := range rows {
		b, _ := json.Marshal(r)
		k, _ := blockKeyOf(r)
		store.SaveUnsentPayload(k, b, now)
	}

	cl := &fakeAtlasClient{enabled: true, status: 200}
	republishSweep(context.Background(), store, cl)

	if cl.sendCall != 1 {
		t.Fatalf("want exactly one batch (3 rows fits under republishBatchSize), got %d calls", cl.sendCall)
	}
	got := cl.batches[0]
	if len(got) != 3 {
		t.Fatalf("want 3 rows in the batch, got %d", len(got))
	}
	wantStarts := []string{
		time.Unix(1000, 0).UTC().Format(time.RFC3339),
		time.Unix(2000, 0).UTC().Format(time.RFC3339),
		time.Unix(3000, 0).UTC().Format(time.RFC3339),
	}
	for i, w := range wantStarts {
		if got[i].Window.Start != w {
			t.Fatalf("position %d: want start %s, got %s", i, w, got[i].Window.Start)
		}
	}

	remaining, err := store.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("delivered payloads must be cleared, got %d remaining", len(remaining))
	}

	snap, err := store.Read(time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Blocks) != 3 {
		t.Fatalf("want 3 block rows recorded, got %d", len(snap.Blocks))
	}
	for _, b := range snap.Blocks {
		if b.Cells["sent"] == nil || b.Cells["sent"]["status"] != "ok" {
			t.Fatalf("sent cell not marked ok: %+v", b.Cells["sent"])
		}
		if b.Cells["received"] == nil || b.Cells["received"]["status"] != "ok" {
			t.Fatalf("received cell not marked ok: %+v", b.Cells["received"])
		}
	}
}

// An Atlas that cannot be REACHED (5xx, network) ends the sweep without
// touching the payload's refusal count. Remove this and an outage — which says
// nothing whatever about the blocks — spends the bound that exists for
// genuinely unacceptable payloads, and a week offline would hold every
// captured block aside permanently.
func TestUnreachableAtlasEndsTheSweepAndCountsNoRefusal(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	row := blockRow("sess-fail", 500)
	b, _ := json.Marshal(row)
	k, _ := blockKeyOf(row)
	store.SaveUnsentPayload(k, b, time.Now())

	cl := &fakeAtlasClient{enabled: true, sendErr: &retry.StatusError{Code: 500}, failFirstN: 999}
	if got := republishSweep(context.Background(), store, cl); got != sweepBlocked {
		t.Fatalf("outcome = %v, want sweepBlocked — an unreachable Atlas must leave the loop retrying", got)
	}
	if cl.sendCall != 1 {
		t.Fatalf("want ONE attempt per sweep (the loop is what retries, not an inner spin), got %d", cl.sendCall)
	}

	remaining, err := store.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Fatal("a payload that never delivered must remain captured for the next sweep")
	}
	if remaining[0].Refusals != 0 {
		t.Fatalf("refusals = %d, want 0 — 5xx is not a verdict on the payload", remaining[0].Refusals)
	}
}

// A CREDENTIAL rejection is machine-wide: every remaining batch would be told
// the same thing, so the sweep stops rather than working through the backlog
// collecting identical 401s — and it must not be read as a verdict on any
// payload. The three-way split (rejected / unavailable / refused) is the
// daemon's existing drain shape; collapsing any two of them is what this
// defends.
func TestACredentialRejectionStopsTheSweepAndCountsNoRefusal(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	for i := int64(0); i < 12; i++ { // two batches' worth
		row := blockRow("sess-401", 1000+i)
		b, _ := json.Marshal(row)
		k, _ := blockKeyOf(row)
		store.SaveUnsentPayload(k, b, time.Now())
	}

	cl := &fakeAtlasClient{enabled: true, sendErr: &retry.StatusError{Code: 401}, failFirstN: 999}
	if got := republishSweep(context.Background(), store, cl); got != sweepBlocked {
		t.Fatalf("outcome = %v, want sweepBlocked", got)
	}
	if cl.sendCall != 1 {
		t.Fatalf("want the sweep to stop on the FIRST rejection, got %d calls", cl.sendCall)
	}
	remaining, err := store.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 12 {
		t.Fatalf("want all 12 payloads kept, got %d", len(remaining))
	}
	for _, p := range remaining {
		if p.Refusals != 0 {
			t.Fatalf("%+v: a rejected credential must not count against a payload", p.Key)
		}
	}
}

// --- against a real HTTP Atlas -------------------------------------------
//
// The tests below drive a REAL atlas.Live over httptest rather than the fake
// above, because the thing under test is partly atlas.Live itself: this sweep
// is the only caller of atlas.Client.SendBlocks in a running daemon, so
// LastResponse — the sole input to the health strip's `atlas` row — moves only
// when this code moves it. A fake whose LastResponse is hardcoded to zero (the
// one above) cannot see that, which is exactly why the stale "batch refused"
// chip survived the existing suite.

// atlasOver builds the live connector against a test server, plus a recorder
// of what each request carried.
func atlasOver(t *testing.T, h func(w http.ResponseWriter, sessions []string)) (*atlas.Live, func() [][]string) {
	t.Helper()
	var mu sync.Mutex
	var seen [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var env struct {
			Blocks []struct {
				SessionID string `json:"session_id"`
			} `json:"blocks"`
		}
		_ = json.NewDecoder(r.Body).Decode(&env)
		sessions := make([]string, 0, len(env.Blocks))
		for _, b := range env.Blocks {
			sessions = append(sessions, b.SessionID)
		}
		mu.Lock()
		seen = append(seen, sessions)
		mu.Unlock()
		h(w, sessions)
	}))
	t.Cleanup(srv.Close)
	return &atlas.Live{Blocks: publish.New(srv.URL, func() string { return "tok" }, "actor")},
		func() [][]string {
			mu.Lock()
			defer mu.Unlock()
			return append([][]string(nil), seen...)
		}
}

// stripAtlasRow is startHealth's own mapping from LastResponse to the health
// row the page renders (v3health.go: zero instant = unknown, 2xx = ok,
// anything else = failed with classifyAtlasStatus). Mirrored here rather than
// called, because startHealth needs a whole *v3; if the two ever drift, these
// tests are the tell.
func stripAtlasRow(cl atlas.Client) (ledger.Status, ledger.Reason) {
	status, at := cl.LastResponse()
	if at.IsZero() {
		return "", "" // unknown: never tried
	}
	if status >= 200 && status < 300 {
		return ledger.StatusOK, ledger.ReasonNone
	}
	return ledger.StatusFailed, classifyAtlasStatus(status)
}

func captureBlocks(t *testing.T, store *ledger.Store, sessions ...string) {
	t.Helper()
	for i, s := range sessions {
		row := blockRow(s, int64(1_000_000+i*600))
		b, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		k, ok := blockKeyOf(row)
		if !ok {
			t.Fatalf("could not key %s", s)
		}
		store.SaveUnsentPayload(k, b, time.Now())
	}
}

func receivedCells(t *testing.T, store *ledger.Store) map[string]map[string]any {
	t.Helper()
	snap, err := store.Read(time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]any{}
	for _, b := range snap.Blocks {
		out[b.Key.Session] = b.Cells["received"]
	}
	return out
}

// A batch Atlas accepts drains the captured table AND leaves the health
// strip's atlas row reading ok. Both halves matter: draining without moving
// LastResponse would deliver the blocks and leave the chip lying.
func TestAnAcceptedBatchDrainsTheCapturedBlocksAndClearsTheAtlasRow(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	captureBlocks(t, store, "sess-ok-1", "sess-ok-2")
	cl, requests := atlasOver(t, func(w http.ResponseWriter, _ []string) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true,"stored":2}`))
	})

	if got := republishSweep(context.Background(), store, cl); got != sweepDone {
		t.Fatalf("outcome = %v, want sweepDone", got)
	}
	if n := len(requests()); n != 1 {
		t.Fatalf("want one batch POST, got %d", n)
	}
	remaining, err := store.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("delivered payloads must be cleared, %d remain", len(remaining))
	}
	status, reason := stripAtlasRow(cl)
	if status != ledger.StatusOK || reason != ledger.ReasonNone {
		t.Fatalf("health strip atlas row = (%s, %s), want (ok, ) — a delivered batch must clear it",
			status, reason)
	}
}

// THE STORY. A refusal that Atlas has since stopped giving must stop showing
// as refused, and the captured blocks must drain, with nobody restarting
// anything.
//
// ⚠️ Remove this and the shipped behaviour is what was measured on a real
// machine: one transient 422, five attempts inside a single startup sweep,
// then `return` — 41 blocks captured forever and a "batch refused" chip that
// never moved again, while the very same payloads were being accepted 201 by
// the very same Atlas. Nothing but a daemon restart could clear it.
func TestATransientRefusalThatAtlasHasSinceStoppedGivingStopsShowingAsRefused(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	defer swapSweepCadence(time.Millisecond, 4*time.Millisecond)()
	store := ledger.New()
	captureBlocks(t, store, "sess-transient")

	var calls int32
	cl, _ := atlasOver(t, func(w http.ResponseWriter, _ []string) {
		if atomic.AddInt32(&calls, 1) <= 2 { // the batch POST and its isolation retry
			w.WriteHeader(http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true,"stored":1}`))
	})

	// While the refusals are being given, the strip is RIGHT to say refused.
	republishSweep(context.Background(), store, cl)
	if _, reason := stripAtlasRow(cl); reason != ledger.ReasonAtlasRefused {
		t.Fatalf("during the refusal the strip must say refused, got %s", reason)
	}

	runLoopToCompletion(t, store, cl)

	remaining, err := store.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("the captured block must drain once Atlas accepts it again, %d remain", len(remaining))
	}
	status, reason := stripAtlasRow(cl)
	if status != ledger.StatusOK || reason != ledger.ReasonNone {
		t.Fatalf("health strip atlas row = (%s, %s), want (ok, ) — a batch Atlas has since accepted "+
			"must stop showing as refused", status, reason)
	}
	if cell := receivedCells(t, store)["sess-transient"]; cell == nil || cell["status"] != "ok" {
		t.Fatalf("received cell = %+v, want ok", cell)
	}
}

// THE NEGATIVE CASE, and the one that must not regress. A payload Atlas
// genuinely will not take is retried a BOUNDED number of times, then held
// aside — never deleted, never retried again, and still reading refused
// everywhere a person looks.
//
// Remove any part of this and the fix above becomes an unbounded retry loop
// against a server that will never say yes.
func TestAPersistentlyRefusedBlockStaysRefusedAndIsNotRetriedForever(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	defer swapSweepCadence(time.Millisecond, 4*time.Millisecond)()
	store := ledger.New()
	captureBlocks(t, store, "sess-bad")
	cl, requests := atlasOver(t, func(w http.ResponseWriter, _ []string) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	})

	runLoopToCompletion(t, store, cl) // must TERMINATE: that is half the claim

	// Bounded: one batch POST plus one isolation POST per sweep, for exactly
	// the refusal limit's worth of sweeps.
	if n := len(requests()); n != 2*ledger.UnsentRefusalLimit {
		t.Fatalf("made %d POSTs, want %d — the retry must be bounded by the refusal limit",
			n, 2*ledger.UnsentRefusalLimit)
	}
	// Kept, not deleted, and no longer offered to the drain.
	offered, err := store.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(offered) != 0 {
		t.Fatalf("a held-aside payload must not be offered again, got %+v", offered)
	}
	total, heldAside, err := store.UnsentCounts()
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || heldAside != 1 {
		t.Fatalf("counts = (%d, %d), want (1, 1) — held aside, never deleted", total, heldAside)
	}
	// Still refused, on the block's own row AND on the health strip.
	cell := receivedCells(t, store)["sess-bad"]
	if cell == nil || cell["status"] != "failed" || cell["reason"] != string(ledger.ReasonAtlasRefused) {
		t.Fatalf("received cell = %+v, want failed/atlas_refused", cell)
	}
	status, reason := stripAtlasRow(cl)
	if status != ledger.StatusFailed || reason != ledger.ReasonAtlasRefused {
		t.Fatalf("health strip atlas row = (%s, %s), want (failed, atlas_refused) — a genuine refusal "+
			"must keep saying so", status, reason)
	}
}

// PER-BLOCK ISOLATION. A refused BATCH names the envelope, never the row, so
// the seven blocks Atlas would happily take must not be held behind the one it
// will not. Without this the measured incident's shape returns at batch scale:
// 41 good blocks stuck behind a single batch verdict.
func TestOneRefusedBlockDoesNotHoldTheRestOfItsBatch(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	captureBlocks(t, store, "sess-good-a", "sess-poison", "sess-good-b")
	cl, requests := atlasOver(t, func(w http.ResponseWriter, sessions []string) {
		for _, s := range sessions {
			if s == "sess-poison" {
				w.WriteHeader(http.StatusUnprocessableEntity)
				return
			}
		}
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	republishSweep(context.Background(), store, cl)

	// One batch of 3, then three solo posts.
	got := requests()
	if len(got) != 4 || len(got[0]) != 3 {
		t.Fatalf("want a batch of 3 then 3 solo posts, got %+v", got)
	}
	for _, r := range got[1:] {
		if len(r) != 1 {
			t.Fatalf("isolation must post ONE block at a time, got %+v", r)
		}
	}
	remaining, err := store.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].Key.Session != "sess-poison" {
		t.Fatalf("only the refused block may remain captured, got %+v", remaining)
	}
	if remaining[0].Refusals != 1 {
		t.Fatalf("refusals = %d, want 1 — a solo refusal is the only kind that counts",
			remaining[0].Refusals)
	}
	cells := receivedCells(t, store)
	for _, s := range []string{"sess-good-a", "sess-good-b"} {
		if cells[s] == nil || cells[s]["status"] != "ok" {
			t.Fatalf("%s received cell = %+v, want ok", s, cells[s])
		}
	}
}

// A payload may be refused at most ONCE per sweep. It is not deleted, so the
// next batch this sweep asks for still leads with it; without the guard the
// loop would re-offer and re-refuse it until the limit was spent, in
// milliseconds — turning a bound that is meant to count occasions spread over
// hours into one that counts requests.
func TestAPayloadIsRefusedAtMostOncePerSweep(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	captureBlocks(t, store, "sess-bad")
	cl, requests := atlasOver(t, func(w http.ResponseWriter, _ []string) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	})

	republishSweep(context.Background(), store, cl)

	if n := len(requests()); n != 2 {
		t.Fatalf("one sweep made %d POSTs, want 2 (the batch and its one isolation retry)", n)
	}
	remaining, err := store.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining[0].Refusals != 1 {
		t.Fatalf("want exactly one refusal recorded in one sweep, got %+v", remaining)
	}
}

// RE-DELIVERY IS NOT A DUPLICATE. A block's identity is (session, start) and
// Atlas upserts on it (publish.BuildBlock: "a re-delivery after a failed
// publish ... costs nothing but bandwidth"), which is what makes retrying safe
// at all. The client-side half of that claim is what this pins: the ledger
// keys on the same pair, so a block sent twice is one row with one set of
// cells, not two rows and not a double count.
func TestReDeliveringABlockAtlasAlreadyStoredDoesNotDuplicateTheLedgerRow(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	cl, requests := atlasOver(t, func(w http.ResponseWriter, _ []string) {
		w.Header().Set("content-type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	})

	captureBlocks(t, store, "sess-twice")
	republishSweep(context.Background(), store, cl)
	captureBlocks(t, store, "sess-twice") // the SAME (session, start) captured again
	republishSweep(context.Background(), store, cl)

	if n := len(requests()); n != 2 {
		t.Fatalf("want the block offered twice, got %d POSTs", n)
	}
	snap, err := store.Read(time.Time{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Blocks) != 1 {
		t.Fatalf("want ONE ledger row for a re-delivered block, got %d", len(snap.Blocks))
	}
	if cell := snap.Blocks[0].Cells["received"]; cell == nil || cell["status"] != "ok" {
		t.Fatalf("received cell = %+v, want ok", cell)
	}
	remaining, err := store.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 0 {
		t.Fatalf("want the captured copy cleared both times, %d remain", len(remaining))
	}
}

// --- helpers -------------------------------------------------------------

// swapSweepCadence shrinks the sweep interval so a test can prove a LATER
// sweep happens without spending five real minutes on it.
func swapSweepCadence(base, max time.Duration) func() {
	oldBase, oldMax := republishInterval, republishMaxInterval
	republishInterval, republishMaxInterval = base, max
	return func() { republishInterval, republishMaxInterval = oldBase, oldMax }
}

// runLoopToCompletion runs the republisher's own loop and requires it to
// RETURN on its own. A loop that has to be cancelled is the unbounded-retry
// failure this whole change must not introduce.
func runLoopToCompletion(t *testing.T, store *ledger.Store, cl atlas.Client) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		republishLoop(ctx, store, cl)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the republish loop never finished — it must stop once nothing remains that it would send")
	}
}
