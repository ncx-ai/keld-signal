package features

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
)

// fakeSource is a scripted analysis service: one answer per call, in order.
type fakeSource struct {
	answers []answer
	calls   []call
}

type answer struct {
	rows      []enrich.FeatureVector
	watermark *float64
	ok        bool
}

type call struct {
	path    string
	since   *float64
	maxRows int
}

func (f *fakeSource) FeatureRowsFor(path, source, sessionID string,
	since *float64, now time.Time, maxRows int,
	resolved enrich.ResolvedFacts) ([]enrich.FeatureVector, *float64, bool) {
	f.calls = append(f.calls, call{path: path, since: since, maxRows: maxRows})
	if len(f.answers) == 0 {
		return nil, nil, false
	}
	a := f.answers[0]
	f.answers = f.answers[1:]
	return a.rows, a.watermark, a.ok
}

func row(anchor string, ts float64) enrich.FeatureVector {
	return enrich.FeatureVector{
		SessionID: "S1", Source: "claude_code", Anchor: anchor,
		TS: time.Unix(int64(ts), 0).UTC().Format(time.RFC3339Nano), TSUnix: ts,
		FeatureSpec: 1,
		Structured:  &enrich.QuantisedVector{Dims: 2, Scale: 0.01, Q: []byte{1, 2}},
	}
}

func f64(v float64) *float64 { return &v }

func newTestEmitter(t *testing.T, src Source, gate func() bool) *Emitter {
	t.Helper()
	return New(src, nil, gate, "dg@keld.co", filepath.Join(t.TempDir(), "features.json"))
}

// ⚠️ FIRST SIGHT IS FORWARD-ONLY, and here that matters more than on the block
// path: a backfill would mean an encoder forward pass per message of every
// transcript on the machine — 496 of them in the reference corpus.
func TestFirstSightSeedsForwardOnlyAndCollectsNothing(t *testing.T) {
	src := &fakeSource{answers: []answer{
		{watermark: f64(1000), ok: true},
		{rows: []enrich.FeatureVector{row("bin", 1300)}, ok: true},
	}}
	e := newTestEmitter(t, src, nil)
	e.advanceAt("claude_code", "/t/S1.jsonl", time.Unix(1000, 0))

	if n := e.Sweep(context.Background(), time.Unix(1010, 0)); n != 0 {
		t.Fatalf("first sight buffered %d rows, want 0", n)
	}
	if src.calls[0].since != nil {
		t.Fatalf("first sight sent a cursor: %v", *src.calls[0].since)
	}
	if src.calls[0].maxRows != 0 {
		t.Fatalf("first sight asked for %d rows, want 0", src.calls[0].maxRows)
	}

	if n := e.Sweep(context.Background(), time.Unix(1020, 0)); n != 1 {
		t.Fatalf("second sweep buffered %d rows, want 1", n)
	}
	if src.calls[1].since == nil || *src.calls[1].since != 1000 {
		t.Fatalf("second sweep did not resume from the watermark: %v", src.calls[1].since)
	}
}

// A transcript whose store has never been ingested has a nil watermark, and a
// zero cursor is a real instant in 1970 — seeding at zero would backfill
// everything.
func TestNilWatermarkLeavesTheCursorUnseeded(t *testing.T) {
	src := &fakeSource{answers: []answer{{ok: true}, {ok: true}}}
	e := newTestEmitter(t, src, nil)
	e.advanceAt("claude_code", "/t/S1.jsonl", time.Unix(1000, 0))

	e.Sweep(context.Background(), time.Unix(1010, 0))
	e.Sweep(context.Background(), time.Unix(1020, 0))
	for i, c := range src.calls {
		if c.since != nil {
			t.Fatalf("call %d sent a cursor %v against a nil watermark", i, *c.since)
		}
	}
}

// The sidecar could not answer — not ready, restarting, or (until step 2 lands)
// the route does not exist. Hold the cursor and ask again; never advance past
// ground nobody answered for.
func TestAnUnansweredSweepHoldsTheCursor(t *testing.T) {
	src := &fakeSource{answers: []answer{
		{watermark: f64(1000), ok: true},
		{ok: false},
		{rows: []enrich.FeatureVector{row("bin", 1300)}, ok: true},
	}}
	e := newTestEmitter(t, src, nil)
	e.advanceAt("claude_code", "/t/S1.jsonl", time.Unix(1000, 0))
	e.Sweep(context.Background(), time.Unix(1010, 0))
	e.Sweep(context.Background(), time.Unix(1020, 0))
	e.Sweep(context.Background(), time.Unix(1030, 0))

	if got := *src.calls[2].since; got != 1000 {
		t.Fatalf("cursor moved past an unanswered sweep: %v", got)
	}
	if e.Buffered() != 1 {
		t.Fatalf("buffered = %d, want 1", e.Buffered())
	}
}

// ⚠️ BACKPRESSURE IS WHAT MAKES ADVANCING ON BUFFERING SAFE. A full buffer must
// take nothing at all rather than drop the oldest, because the cursor has
// already moved past whatever was dropped and the sidecar answers `since_ts` —
// it would never offer those rows again.
func TestAFullBufferTakesNothingAndHoldsTheCursor(t *testing.T) {
	full := make([]enrich.FeatureVector, bufferCapacity)
	for i := range full {
		full[i] = row("bin", float64(2000+i))
	}
	src := &fakeSource{answers: []answer{
		{watermark: f64(1000), ok: true},
	}}
	// Enough answers to fill the buffer maxPerSweep rows at a time, then one
	// more sweep against a full buffer.
	for i := 0; i < bufferCapacity/maxPerSweep; i++ {
		src.answers = append(src.answers, answer{rows: full[i*maxPerSweep : (i+1)*maxPerSweep], ok: true})
	}
	e := newTestEmitter(t, src, nil)
	e.advanceAt("claude_code", "/t/S1.jsonl", time.Unix(1000, 0))
	for i := 0; i < 1+bufferCapacity/maxPerSweep; i++ {
		e.Sweep(context.Background(), time.Unix(int64(1010+i), 0))
	}
	if e.Buffered() != bufferCapacity {
		t.Fatalf("buffered = %d, want %d", e.Buffered(), bufferCapacity)
	}

	callsBefore := len(src.calls)
	if n := e.Sweep(context.Background(), time.Unix(9999, 0)); n != 0 {
		t.Fatalf("a full buffer took %d rows", n)
	}
	if len(src.calls) != callsBefore {
		t.Fatalf("a full buffer still called the sidecar %d times", len(src.calls)-callsBefore)
	}

	// And a sweep never asks for more than there is room for.
	for i, c := range src.calls[1:] {
		if c.maxRows > maxPerSweep || c.maxRows <= 0 {
			t.Fatalf("call %d asked for %d rows", i+1, c.maxRows)
		}
	}
}

// The buffer is the hand-off to the reporter; the emitter never writes to a
// drained slice again.
func TestDrainEmptiesTheBuffer(t *testing.T) {
	src := &fakeSource{answers: []answer{
		{watermark: f64(1000), ok: true},
		{rows: []enrich.FeatureVector{row("bin", 1300), row("block", 1400)}, ok: true},
	}}
	e := newTestEmitter(t, src, nil)
	e.advanceAt("claude_code", "/t/S1.jsonl", time.Unix(1000, 0))
	e.Sweep(context.Background(), time.Unix(1010, 0))
	e.Sweep(context.Background(), time.Unix(1020, 0))

	got := e.Drain()
	if len(got) != 2 {
		t.Fatalf("drained %d rows, want 2", len(got))
	}
	if e.Buffered() != 0 {
		t.Fatalf("buffer not emptied: %d", e.Buffered())
	}
	if e.Drain() != nil {
		t.Fatal("a second drain returned rows")
	}
	if got[0].Correlation.Scheme != enrich.FeatureCorrScheme {
		t.Fatalf("drained row is not a feature row: %+v", got[0].Correlation)
	}
}

// The settling sweep is what buys the trailing rows an activity-driven emitter
// would never collect; the grace is what stops it retiring one sweep before
// they appear.
func TestTranscriptRetiresOnlyAfterTheIdleThresholdPlusGrace(t *testing.T) {
	src := &fakeSource{answers: []answer{
		{watermark: f64(1000), ok: true}, {ok: true}, {ok: true},
	}}
	e := newTestEmitter(t, src, nil)
	seen := time.Unix(1000, 0)
	e.advanceAt("claude_code", "/t/S1.jsonl", seen)
	e.Sweep(context.Background(), seen.Add(time.Second))

	e.Sweep(context.Background(), seen.Add(idleSeconds+settleGrace-time.Minute))
	if e.ActiveCount() != 1 {
		t.Fatal("retired before the idle threshold plus grace")
	}
	e.Sweep(context.Background(), seen.Add(idleSeconds+settleGrace+time.Minute))
	if e.ActiveCount() != 0 {
		t.Fatal("did not retire after the idle threshold plus grace")
	}
}

// ⚠️ BOTH TOGGLES DEFAULT OFF. A closed gate must take nothing and, crucially,
// must not even enter transcripts into the active set — the whole subsystem is
// inert until something switches it on.
func TestTheGateDefaultsOffAndAClosedGateCollectsNothing(t *testing.T) {
	// Nothing set: the local default is off in both directions.
	t.Setenv(EnvEnabled, "")
	t.Setenv(EnvPublish, "")
	if Enabled(false) || PublishEnabled(false) {
		t.Fatal("the toggles do not default off")
	}

	src := &fakeSource{answers: []answer{{watermark: f64(1000), ok: true}}}
	off := func() bool { return false }
	e := newTestEmitter(t, src, off)
	e.advanceAt("claude_code", "/t/S1.jsonl", time.Unix(1000, 0))
	if e.ActiveCount() != 0 {
		t.Fatal("a closed gate still tracked a transcript")
	}
	if n := e.Sweep(context.Background(), time.Unix(1010, 0)); n != 0 {
		t.Fatalf("a closed gate buffered %d rows", n)
	}
	if len(src.calls) != 0 {
		t.Fatalf("a closed gate called the sidecar %d times", len(src.calls))
	}
}

// The cursor is persisted atomically after every sweep that moved anything;
// losing the file makes every transcript first-sight again, which is
// forward-only and therefore a real loss.
func TestTheCursorSurvivesARestart(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "features.json")

	src := &fakeSource{answers: []answer{
		{watermark: f64(1000), ok: true},
		{rows: []enrich.FeatureVector{row("bin", 1300)}, ok: true},
	}}
	e := New(src, nil, nil, "a", file)
	e.advanceAt("claude_code", "/t/S1.jsonl", time.Unix(1000, 0))
	e.Sweep(context.Background(), time.Unix(1010, 0))
	e.Sweep(context.Background(), time.Unix(1020, 0))

	src2 := &fakeSource{answers: []answer{{ok: true}}}
	e2 := New(src2, nil, nil, "a", file)
	e2.advanceAt("claude_code", "/t/S1.jsonl", time.Unix(1030, 0))
	e2.Sweep(context.Background(), time.Unix(1040, 0))
	if len(src2.calls) != 1 || src2.calls[0].since == nil || *src2.calls[0].since != 1300 {
		t.Fatalf("cursor not restored: %+v", src2.calls)
	}
}

// A monotone cursor is what stops a rolled-back store or a late answer
// re-offering settled ground — which here costs an encoder forward pass per
// message, not just bandwidth.
func TestTheCursorIsMonotone(t *testing.T) {
	s := newState(filepath.Join(t.TempDir(), "s.json"))
	s.note("claude_code", "S1", "/t/S1.jsonl", time.Unix(0, 0))
	s.advance("/t/S1.jsonl", 500)
	s.advance("/t/S1.jsonl", 400)
	tg := s.targets([]string{"/t/S1.jsonl"})
	if len(tg) != 1 || tg[0].Cursor == nil || *tg[0].Cursor != 500 {
		t.Fatalf("cursor went backwards: %+v", tg)
	}
}

// A corrupt state file must not fail a daemon start; the cost is stated
// (forward-only re-seed) rather than paid as a crash.
func TestACorruptStateFileStartsFresh(t *testing.T) {
	file := filepath.Join(t.TempDir(), "features.json")
	if err := writeFile(file, "{not json"); err != nil {
		t.Fatal(err)
	}
	s := newState(file)
	if len(s.entries) != 0 {
		t.Fatalf("entries = %d, want 0", len(s.entries))
	}
}

// --- reporter -------------------------------------------------------------

type fakeTransport struct {
	bodies [][]byte
	err    error
	drains int
}

func (f *fakeTransport) Deliver(ctx context.Context, body []byte) error {
	if f.err != nil {
		return f.err
	}
	f.bodies = append(f.bodies, append([]byte(nil), body...))
	return nil
}
func (f *fakeTransport) DrainSpool(ctx context.Context) error { f.drains++; return nil }

func rows(n int) []publish.FeatureRow {
	out := make([]publish.FeatureRow, n)
	for i := range out {
		out[i] = publish.BuildFeature(row("bin", float64(1000+i)), "a", time.Unix(0, 0))
	}
	return out
}

// Chunking is a DURABILITY parameter: one POST is one all-or-nothing outcome
// and one spool file, so it is the granularity at which progress is kept when
// the link drops mid-drain.
func TestFlushChunksTheBatch(t *testing.T) {
	tr := &fakeTransport{}
	pending := rows(batchRows + 3)
	r := NewReporter(tr, func() []publish.FeatureRow {
		out := pending
		pending = nil
		return out
	}, "inst-1", nil)

	if err := r.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(tr.bodies) != 2 {
		t.Fatalf("posted %d bodies, want 2", len(tr.bodies))
	}
	var first, second publish.FeaturesEnvelope
	if err := json.Unmarshal(tr.bodies[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(tr.bodies[1], &second); err != nil {
		t.Fatal(err)
	}
	if len(first.Features) != batchRows || len(second.Features) != 3 {
		t.Fatalf("chunk sizes = %d, %d", len(first.Features), len(second.Features))
	}
	if first.InstallID != "inst-1" {
		t.Fatalf("install id = %q", first.InstallID)
	}
}

// ⚠️ THE PUBLISH GATE IS SEPARATE FROM THE COLLECT GATE, and a closed publish
// gate must still DRAIN — a buffer nobody empties wedges the emitter's
// backpressure and stops it collecting at all.
func TestAClosedPublishGateDrainsButSendsNothing(t *testing.T) {
	tr := &fakeTransport{}
	drained := 0
	r := NewReporter(tr, func() []publish.FeatureRow {
		drained++
		return rows(3)
	}, "inst-1", func() bool { return false })

	if err := r.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if drained != 1 {
		t.Fatalf("drained %d times, want 1", drained)
	}
	if len(tr.bodies) != 0 {
		t.Fatalf("a closed publish gate sent %d bodies", len(tr.bodies))
	}
}

// Continuing past a failing chunk would push chunk after chunk into a spool
// already dropping its oldest entries — one outage becoming an eviction storm.
func TestFlushStopsAtTheFirstFailingChunk(t *testing.T) {
	tr := &fakeTransport{err: errors.New("atlas down")}
	r := NewReporter(tr, func() []publish.FeatureRow { return rows(batchRows * 3) }, "i", nil)
	if err := r.Flush(context.Background()); err == nil {
		t.Fatal("flush did not report the failure")
	}
	if len(tr.bodies) != 0 {
		t.Fatalf("posted %d bodies past a failure", len(tr.bodies))
	}
}

// An empty buffer must not POST at all: a five-minute ticker on a quiet machine
// would otherwise be a five-minute empty request.
func TestFlushOnAnEmptyBufferPostsNothing(t *testing.T) {
	tr := &fakeTransport{}
	r := NewReporter(tr, func() []publish.FeatureRow { return nil }, "i", nil)
	if err := r.Flush(context.Background()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if len(tr.bodies) != 0 {
		t.Fatalf("posted %d bodies on an empty buffer", len(tr.bodies))
	}
}

// writeFile is a tiny helper so the corrupt-state test reads as one line.
func writeFile(path, body string) error { return os.WriteFile(path, []byte(body), 0o600) }
