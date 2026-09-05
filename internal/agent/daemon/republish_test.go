package daemon

import (
	"context"
	"encoding/json"
	"sync"
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
func (f *fakeAtlasClient) Workstreams(context.Context) ([]atlas.Workstream, error) { return nil, nil }
func (f *fakeAtlasClient) PatchWorkstream(context.Context, string, []atlas.Value) error {
	return nil
}
func (f *fakeAtlasClient) RedeemCode(context.Context, string) (atlas.Paired, error) {
	return atlas.Paired{}, nil
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

func TestRepublishSweepRetriesThenGivesUpAndKeepsThePayload(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	orig := republishBaseBackoff
	republishBaseBackoff = 10 * time.Millisecond
	defer func() { republishBaseBackoff = orig }()
	store := ledger.New()
	row := blockRow("sess-fail", 500)
	b, _ := json.Marshal(row)
	k, _ := blockKeyOf(row)
	store.SaveUnsentPayload(k, b, time.Now())

	cl := &fakeAtlasClient{enabled: true, sendErr: &retry.StatusError{Code: 500}, failFirstN: 999}
	start := time.Now()
	republishSweep(context.Background(), store, cl)
	elapsed := time.Since(start)

	if cl.sendCall != republishMaxAttempts {
		t.Fatalf("want exactly %d attempts, got %d", republishMaxAttempts, cl.sendCall)
	}
	// Backoff is exponential from republishBaseBackoff; just sanity-check it
	// actually waited between attempts rather than busy-looping.
	if elapsed < republishBaseBackoff {
		t.Fatalf("want the sweep to have backed off between attempts, elapsed=%s", elapsed)
	}

	remaining, err := store.UnsentPayloads(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 {
		t.Fatal("a payload that never delivered must remain captured for the next attempt")
	}
}
