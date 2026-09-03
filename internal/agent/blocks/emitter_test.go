package blocks

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
)

// fakeDig stands in for the sidecar's POST /blocks and reproduces the one piece
// of its behaviour the emitter's correctness rests on: `since_ts` is compared
// against a block's START with `>=`, so the caller resumes by passing the last
// emitted block's END. A fake that ignored `since` would make the
// no-duplicates test vacuous.
type fakeDig struct {
	mu        sync.Mutex
	all       []enrich.BlockCharacterisation
	watermark *float64
	fail      bool
	calls     []digCall
}

type digCall struct {
	Path      string
	Since     *float64
	MaxBlocks int
}

func (f *fakeDig) BlocksCharacterised(path, source, sessionID string,
	since *float64, now time.Time, maxBlocks int,
	resolved enrich.ResolvedFacts) ([]enrich.BlockCharacterisation, *float64, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, digCall{Path: path, Since: since, MaxBlocks: maxBlocks})
	if f.fail {
		return nil, nil, false
	}
	var out []enrich.BlockCharacterisation
	for _, b := range f.all {
		if since != nil && b.StartTS < *since {
			continue
		}
		if len(out) >= maxBlocks {
			break
		}
		out = append(out, b)
	}
	return out, f.watermark, true
}

func (f *fakeDig) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeDig) lastCall() digCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[len(f.calls)-1]
}

// fakeSender records every batch and can be told to start failing from the Nth
// one, which is how a mid-drain network drop is exercised.
type fakeSender struct {
	mu        sync.Mutex
	batches   [][]publish.BlockEnrichment
	failFrom  int // 0 = never fail; N = the Nth batch (1-based) and every one after
	callCount int
}

func (s *fakeSender) SendBlocks(rows []publish.BlockEnrichment) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.callCount++
	if s.failFrom > 0 && s.callCount >= s.failFrom {
		return errors.New("atlas is unreachable")
	}
	s.batches = append(s.batches, rows)
	return nil
}

func (s *fakeSender) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, b := range s.batches {
		for _, r := range b {
			out = append(out, r.Correlation.ID)
		}
	}
	return out
}

func block(startTS float64) enrich.BlockCharacterisation {
	end := startTS + 1200
	return enrich.BlockCharacterisation{
		SessionID: "sess",
		Source:    "claude_code",
		Ref: enrich.BlockRef{
			Start: time.Unix(int64(startTS), 0).UTC().Format(time.RFC3339),
			End:   time.Unix(int64(end), 0).UTC().Format(time.RFC3339),
			// A block a reader can place: 20 minutes, real evidence, real reasons.
			SpanMinutes: 20, Evidence: 40, StartReason: "idle", EndReason: "budget",
		},
		StartTS: startTS,
		EndTS:   end,
	}
}

func f64(v float64) *float64 { return &v }

const txPath = "/home/x/.claude/projects/p/sess.jsonl"

// newTestEmitter builds an emitter with the first-sight policy PINNED to
// forward-only, so most of this file exercises cursor and publish semantics
// (which are identical either way) with a deterministic first sweep.
//
// ⚠️ Pinned rather than inherited: New() reads KELD_BLOCKS_BACKFILL, so without
// this a developer's ambient environment would change what these tests measure.
// The backfill default has its own tests, which opt in explicitly.
func newTestEmitter(t *testing.T, dig Digester, pub Sender) *Emitter {
	t.Helper()
	e := New(dig, pub, nil, "actor", filepath.Join(t.TempDir(), "blocks.json"))
	e.backfill = false
	return e
}

// FORWARD-ONLY ON FIRST SIGHT — the KELD_BLOCKS_BACKFILL=0 branch.
//
// ⚠️ THIS WAS THE DEFAULT AND IS NO LONGER. The reasoning was that a machine has
// hundreds of transcripts (496 in the reference corpus) and a daemon restart
// must not emit a herd of history. What it actually cost was every block that
// existed before the moment of install — on this repo's own corpus, 24 closed
// blocks in one transcript, none of which ever reached Atlas — so the default
// is now backfill (see BackfillEnabled). This test keeps the opt-out branch
// honest: the first sweep learns the watermark and emits NOTHING.
func TestFirstSightIsForwardOnlyAndEmitsNothing(t *testing.T) {
	dig := &fakeDig{
		all:       []enrich.BlockCharacterisation{block(1000), block(2200), block(6000)},
		watermark: f64(5000),
	}
	pub := &fakeSender{}
	e := newTestEmitter(t, dig, pub)
	e.backfill = false
	now := time.Unix(9000, 0)

	e.advanceAt("claude_code", txPath, now)
	if n := e.Sweep(context.Background(), now); n != 0 {
		t.Fatalf("first sight published %d rows — it must only seed the cursor", n)
	}
	// It asked for the watermark and NO blocks, and did not backfill.
	if c := dig.lastCall(); c.MaxBlocks != 0 || c.Since != nil {
		t.Fatalf("seeding call = %+v, want max_blocks 0 and a nil since", c)
	}
	if len(pub.sent()) != 0 {
		t.Fatalf("first sight published %v", pub.sent())
	}

	// The next sweep resumes at the watermark: the two blocks that had already
	// happened stay unemitted, and the one after it publishes.
	if n := e.Sweep(context.Background(), now); n != 1 {
		t.Fatalf("second sweep published %d rows, want the one block after the watermark", n)
	}
	if got := pub.sent(); len(got) != 1 || got[0] != "sess@"+block(6000).Ref.Start {
		t.Fatalf("published %v, want only the post-watermark block", got)
	}
}

// A transcript the store has never ingested answers a nil watermark. In the
// KELD_BLOCKS_BACKFILL=0 branch it must be left UNSEEDED rather than seeded at
// zero — zero is a real instant in 1970 and would backfill everything on the
// next sweep. (Under the backfill default there is no seeding call at all, so
// this hazard cannot arise; the branch still exists and is still pinned.)
func TestAnUningestedTranscriptIsLeftUnseeded(t *testing.T) {
	dig := &fakeDig{all: []enrich.BlockCharacterisation{block(1000)}, watermark: nil}
	pub := &fakeSender{}
	e := newTestEmitter(t, dig, pub)
	e.backfill = false
	e.advanceAt("claude_code", txPath, time.Unix(9000, 0))

	e.Sweep(context.Background(), time.Unix(9000, 0))
	e.Sweep(context.Background(), time.Unix(9000, 0))
	if len(pub.sent()) != 0 {
		t.Fatalf("published %v off a store with no watermark", pub.sent())
	}
	// Still asking as a first-sight call, not as a backfill.
	if c := dig.lastCall(); c.MaxBlocks != 0 {
		t.Fatalf("second call = %+v, want another seeding call", c)
	}
}

// THE CURSOR ADVANCES AND RESUMES. A second sweep must not re-publish what the
// first one did, and the ONLY thing preventing it is the cursor being passed
// back as `since_ts`.
func TestTheCursorAdvancesAndResumesWithoutDuplicates(t *testing.T) {
	dig := &fakeDig{all: []enrich.BlockCharacterisation{block(1000), block(2200)}, watermark: f64(900)}
	pub := &fakeSender{}
	e := newTestEmitter(t, dig, pub)
	now := time.Unix(9000, 0)
	e.advanceAt("claude_code", txPath, now)
	e.Sweep(context.Background(), now) // seed at 900

	if n := e.Sweep(context.Background(), now); n != 2 {
		t.Fatalf("published %d, want both blocks", n)
	}
	// The cursor is the LAST EMITTED BLOCK'S END, which the sidecar's `>=`
	// comparison against a block's START turns into "the next one, not this one".
	if c := dig.lastCall(); c.Since == nil || *c.Since != 900 {
		t.Fatalf("the emitting call asked from %v, want the seeded cursor", c.Since)
	}
	if n := e.Sweep(context.Background(), now); n != 0 {
		t.Fatalf("a third sweep re-published %d rows", n)
	}
	if c := dig.lastCall(); c.Since == nil || *c.Since != block(2200).EndTS {
		t.Fatalf("resumed from %v, want the last emitted block's end %v", c.Since, block(2200).EndTS)
	}
	if got := pub.sent(); len(got) != 2 {
		t.Fatalf("published %v — a resumed sweep duplicated work", got)
	}

	// New work arrives: only the new block publishes.
	dig.mu.Lock()
	dig.all = append(dig.all, block(3400))
	dig.mu.Unlock()
	if n := e.Sweep(context.Background(), now); n != 1 {
		t.Fatalf("published %d for one new block", n)
	}
	if got := pub.sent(); len(got) != 3 {
		t.Fatalf("published %v", got)
	}
}

// A cursor must survive a restart, or every transcript becomes first-sight
// again and the blocks between the old cursor and the current watermark are
// never emitted.
// OnPublished is the seam the project-attribution path hangs off. It must
// fire once per successful chunk, with exactly the rows that chunk sent and
// the transcript path, and must never fire for a batch that failed.
func TestOnPublishedFiresAfterEachSuccessfulChunkOnly(t *testing.T) {
	dig := &fakeDig{all: []enrich.BlockCharacterisation{block(1000), block(2200)}, watermark: f64(900)}
	pub := &fakeSender{}
	e := newTestEmitter(t, dig, pub)

	var calls [][]publish.BlockEnrichment
	var paths []string
	e.OnPublished = func(rows []publish.BlockEnrichment, path string) {
		calls = append(calls, rows)
		paths = append(paths, path)
	}

	now := time.Unix(9000, 0)
	e.advanceAt("claude_code", txPath, now)
	e.Sweep(context.Background(), now) // seed at 900
	if n := e.Sweep(context.Background(), now); n != 2 {
		t.Fatalf("published %d, want both blocks", n)
	}
	if len(calls) != 1 || len(calls[0]) != 2 {
		t.Fatalf("OnPublished calls = %+v, want one call with both rows", calls)
	}
	if paths[0] != txPath {
		t.Fatalf("OnPublished path = %q, want %q", paths[0], txPath)
	}

	// A nil hook must not panic — every existing caller left it unset.
	e2 := newTestEmitter(t, dig, pub)
	dig.mu.Lock()
	dig.all = append(dig.all, block(3400))
	dig.mu.Unlock()
	e2.advanceAt("claude_code", txPath, now)
	e2.Sweep(context.Background(), now)
	e2.Sweep(context.Background(), now) // should not panic with OnPublished nil
}

// A failed SendBlocks must never call OnPublished for the rows that failed.
func TestOnPublishedDoesNotFireForAFailedBatch(t *testing.T) {
	dig := &fakeDig{all: []enrich.BlockCharacterisation{block(1000)}, watermark: f64(900)}
	pub := &fakeSender{failFrom: 1}
	e := newTestEmitter(t, dig, pub)
	var calls int
	e.OnPublished = func(rows []publish.BlockEnrichment, path string) { calls++ }

	now := time.Unix(9000, 0)
	e.advanceAt("claude_code", txPath, now)
	e.Sweep(context.Background(), now) // seed
	e.Sweep(context.Background(), now) // fails
	if calls != 0 {
		t.Fatalf("OnPublished fired %d times on a failed batch", calls)
	}
}

func TestTheCursorSurvivesARestart(t *testing.T) {
	file := filepath.Join(t.TempDir(), "blocks.json")
	dig := &fakeDig{all: []enrich.BlockCharacterisation{block(1000)}, watermark: f64(900)}
	pub := &fakeSender{}
	now := time.Unix(9000, 0)

	e := New(dig, pub, nil, "actor", file)
	e.advanceAt("claude_code", txPath, now)
	e.Sweep(context.Background(), now) // seed
	e.Sweep(context.Background(), now) // publish block(1000)
	if len(pub.sent()) != 1 {
		t.Fatalf("premise: %v", pub.sent())
	}

	// Restart: same state file, fresh emitter, empty active set.
	pub2 := &fakeSender{}
	e2 := New(dig, pub2, nil, "actor", file)
	e2.advanceAt("claude_code", txPath, now)
	if n := e2.Sweep(context.Background(), now); n != 0 {
		t.Fatalf("a restarted emitter re-published %d rows", n)
	}
	// It resumed rather than re-seeding: a real query, from the persisted cursor.
	if c := dig.lastCall(); c.MaxBlocks == 0 || c.Since == nil || *c.Since != block(1000).EndTS {
		t.Fatalf("restart call = %+v, want a resume from the persisted cursor", c)
	}
}

// A TRANSCRIPT LEAVES THE ACTIVE SET ONCE SETTLED. This is the whole scale
// argument: work per interval is proportional to ACTIVE transcripts, not to the
// hundreds a machine accumulates.
func TestATranscriptLeavesTheActiveSetOnceSettled(t *testing.T) {
	dig := &fakeDig{watermark: f64(900)}
	e := newTestEmitter(t, dig, &fakeSender{})
	seen := time.Unix(10000, 0)

	// Advance at `seen`, then sweep at various later instants.
	e.advanceAt("claude_code", txPath, seen)
	e.Sweep(context.Background(), seen) // seed
	if e.ActiveCount() != 1 {
		t.Fatal("dropped a transcript on its seeding sweep")
	}

	// Still inside the idle threshold: it must stay, because its trailing block
	// has not settled yet and nothing else will bring it back.
	e.Sweep(context.Background(), seen.Add(idleSeconds-time.Minute))
	if e.ActiveCount() != 1 {
		t.Fatal("retired a transcript before its trailing block could settle")
	}

	// Past idle + grace with nothing closing: settled.
	e.Sweep(context.Background(), seen.Add(idleSeconds+settleGrace+time.Minute))
	if e.ActiveCount() != 0 {
		t.Fatal("a settled transcript stayed in the active set — work would then be " +
			"proportional to KNOWN transcripts, which is the design this rejects")
	}

	// And a new advance puts it back, with its cursor intact.
	before := dig.callCount()
	e.advanceAt("claude_code", txPath, seen.Add(2*time.Hour))
	if e.ActiveCount() != 1 {
		t.Fatal("a retired transcript did not come back on new activity")
	}
	e.Sweep(context.Background(), seen.Add(2*time.Hour))
	if dig.callCount() == before {
		t.Fatal("the returned transcript was not swept")
	}
	if c := dig.lastCall(); c.MaxBlocks == 0 {
		t.Fatal("the returned transcript was re-seeded rather than resumed — its cursor " +
			"must survive leaving the active set")
	}
}

// A transcript that STILL HAS BLOCKS must never be retired, however long the
// last advance was: blocks pending means work pending.
func TestATranscriptWithBlocksIsNeverRetired(t *testing.T) {
	dig := &fakeDig{all: []enrich.BlockCharacterisation{block(1000)}, watermark: f64(900)}
	e := newTestEmitter(t, dig, &fakeSender{})
	seen := time.Unix(10000, 0)
	e.advanceAt("claude_code", txPath, seen)
	e.Sweep(context.Background(), seen)
	e.Sweep(context.Background(), seen.Add(48*time.Hour))
	if e.ActiveCount() != 1 {
		t.Fatal("retired a transcript that had just published a block")
	}
}

// A sidecar that cannot answer must neither advance the cursor nor retire the
// transcript. Queue rather than degrade: the next sweep asks for the same
// ground.
func TestASidecarFailureNeitherAdvancesNorRetires(t *testing.T) {
	dig := &fakeDig{all: []enrich.BlockCharacterisation{block(1000)}, watermark: f64(900)}
	e := newTestEmitter(t, dig, &fakeSender{})
	now := time.Unix(10000, 0)
	e.advanceAt("claude_code", txPath, now)
	e.Sweep(context.Background(), now) // seed at 900

	dig.mu.Lock()
	dig.fail = true
	dig.mu.Unlock()
	e.Sweep(context.Background(), now.Add(24*time.Hour))
	if e.ActiveCount() != 1 {
		t.Fatal("a failed sweep retired the transcript — an unreachable sidecar would " +
			"silently stop the whole path")
	}

	dig.mu.Lock()
	dig.fail = false
	dig.mu.Unlock()
	if n := e.Sweep(context.Background(), now); n != 1 {
		t.Fatalf("published %d after recovery, want the block the failed sweep did not get", n)
	}
}

// ⚠️ THE SILENT-DATA-LOSS RULE. A publish failure must leave the cursor exactly
// where it was, and the next interval must re-offer the SAME blocks. Advancing
// past a failed batch loses those blocks forever: the sidecar answers
// `since_ts` and will never return them again.
func TestAPublishFailureLeavesTheCursorUnmovedAndReOffersTheSameBlocks(t *testing.T) {
	dig := &fakeDig{all: []enrich.BlockCharacterisation{block(1000), block(2200)}, watermark: f64(900)}
	pub := &fakeSender{failFrom: 1}
	e := newTestEmitter(t, dig, pub)
	now := time.Unix(9000, 0)
	e.advanceAt("claude_code", txPath, now)
	e.Sweep(context.Background(), now) // seed at 900

	if n := e.Sweep(context.Background(), now); n != 0 {
		t.Fatalf("counted %d rows as published through a failing sender", n)
	}
	// The NEXT sweep's request is where the cursor actually is: still 900, so
	// the held blocks are asked for again rather than skipped.
	e.Sweep(context.Background(), now)
	if c := dig.lastCall(); c.Since == nil || *c.Since != 900 {
		t.Fatalf("cursor moved to %v on a failed publish — those blocks would be lost "+
			"forever, since the sidecar answers since_ts", c.Since)
	}

	// Atlas comes back. The same blocks are re-fetched and land.
	pub.mu.Lock()
	pub.failFrom = 0
	pub.mu.Unlock()
	if n := e.Sweep(context.Background(), now); n != 2 {
		t.Fatalf("published %d after recovery, want both held blocks", n)
	}
	if got := pub.sent(); len(got) != 2 {
		t.Fatalf("published %v after recovery", got)
	}
}

// PARTIAL BATCH SUCCESS IS THE NORMAL CASE. The cursor advances to the last
// CONTIGUOUS success and stops there — never past a gap, because a watermark
// cannot express a hole.
func TestAPartialDrainAdvancesToTheLastContiguousSuccess(t *testing.T) {
	var all []enrich.BlockCharacterisation
	for i := 0; i < 12; i++ {
		all = append(all, block(float64(1000+i*1200)))
	}
	dig := &fakeDig{all: all, watermark: f64(900)}
	// batchSize is 8, so a 12-block drain is two POSTs. Fail the second.
	pub := &fakeSender{failFrom: 2}
	e := newTestEmitter(t, dig, pub)
	now := time.Unix(90000, 0)
	e.advanceAt("claude_code", txPath, now)
	e.Sweep(context.Background(), now) // seed at 900

	if n := e.Sweep(context.Background(), now); n != batchSize {
		t.Fatalf("published %d, want exactly the first batch to be banked", n)
	}

	pub.mu.Lock()
	pub.failFrom = 0
	pub.mu.Unlock()
	if n := e.Sweep(context.Background(), now); n != len(all)-batchSize {
		t.Fatalf("published %d on recovery, want only the undelivered tail", n)
	}
	// Banked: the recovery sweep asked from the end of the LAST BLOCK OF THE
	// SUCCEEDING BATCH — not from the end of the whole fetch (which would have
	// skipped the undelivered tail) and not from the start (which would have
	// re-sent the banked eight).
	wantCursor := all[batchSize-1].EndTS
	if c := dig.lastCall(); c.Since == nil || *c.Since != wantCursor {
		t.Fatalf("recovery asked from %v, want the last contiguous success %v",
			c.Since, wantCursor)
	}
	// Every block published exactly once across the two sweeps: no gap, no
	// duplicate.
	seen := map[string]int{}
	for _, id := range pub.sent() {
		seen[id]++
	}
	if len(seen) != len(all) {
		t.Fatalf("published %d distinct blocks, want %d", len(seen), len(all))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("block %s published %d times", id, n)
		}
	}
}

// ⚠️ THE PROMPT-ID SEAM IS DELETED, and this pins the absence at the INTERFACE
// rather than at the constructor, because the constructor is only the wiring: a
// `promptIDs []string` left on Digester keeps the sidecar request shape alive
// and would let a future edit re-add the reader without touching this package's
// public surface.
//
// Why it went, so it does not come back: a block is TIME end to end — a cap, a
// silence threshold, a rollup over a span — and Atlas must join
// `event_ts ∈ [start, end)` within the session for cost attribution anyway,
// because a turn spanning several blocks would double-count spend through a
// prompt mapping. That join answers the display question too. It also never
// worked: watch/filter.go yields `promptId`, the store indexes the per-message
// `uuid`, so every real block published an empty mapping.
func TestTheEmitterHasNoPromptIDSeam(t *testing.T) {
	if _, ok := reflect.TypeOf(&Emitter{}).Elem().FieldByName("prompt"); ok {
		t.Error("Emitter still holds a prompt-id reader")
	}
	m, ok := reflect.TypeOf((*Digester)(nil)).Elem().MethodByName("BlocksCharacterised")
	if !ok {
		t.Fatal("Digester lost BlocksCharacterised")
	}
	strs := reflect.TypeOf([]string(nil))
	for i := 0; i < m.Type.NumIn(); i++ {
		if m.Type.In(i) == strs {
			t.Errorf("Digester.BlocksCharacterised still takes a []string at %d — the "+
				"only []string it ever carried was the prompt ids", i)
		}
	}
}

// The emitter is constructible with no prompt-id reader at all: the seam is not
// optional-and-nil, it is gone. A five-argument New is the assertion.
func TestTheEmitterIsConstructedWithoutAnyPromptReader(t *testing.T) {
	dig := &fakeDig{all: []enrich.BlockCharacterisation{block(1000)}, watermark: f64(900)}
	// Deliberately the real New, ambient default and all: this test is about the
	// five-argument signature, and under the backfill default a single sweep
	// publishes rather than seeding.
	t.Setenv(EnvBackfill, "1")
	e := New(dig, &fakeSender{}, nil, "actor", filepath.Join(t.TempDir(), "b.json"))
	now := time.Unix(9000, 0)
	e.advanceAt("claude_code", txPath, now)
	if n := e.Sweep(context.Background(), now); n != 1 {
		t.Fatalf("published %d blocks with no prompt reader, want 1", n)
	}
}

func TestEnabledEnvOverridesConfigBothWays(t *testing.T) {
	// Env unset: the agent-config value decides. This is the path an installer
	// takes — it writes the file and sets no variable anywhere.
	t.Setenv(EnvEnabled, "")
	if !Enabled(true) {
		t.Error("config true, env unset: want enabled")
	}
	if Enabled(false) {
		t.Error("config false, env unset: want disabled")
	}

	// Env on wins over a config that says off.
	for _, v := range []string{"1", "true", "on", "yes"} {
		t.Setenv(EnvEnabled, v)
		if !Enabled(false) {
			t.Errorf("KELD_BLOCKS=%q over config false: want enabled", v)
		}
	}

	// Env off wins over a config that says on. This direction is what lets an
	// operator disable a machine the installer switched on, without editing
	// JSON — and it is the half a one-directional toggle would silently lose.
	for _, v := range []string{"0", "false", "off", "no"} {
		t.Setenv(EnvEnabled, v)
		if Enabled(true) {
			t.Errorf("KELD_BLOCKS=%q over config true: want disabled", v)
		}
	}

	// An unrecognised value is not an opinion: fall through to the config.
	t.Setenv(EnvEnabled, "maybe")
	if !Enabled(true) {
		t.Error("unrecognised env value should fall through to config true")
	}
}

// BACKFILL ON FIRST SIGHT. The forward-only default above means the history a
// machine already has is never emitted — on a fresh v2 install that is every
// block before the moment of install, and on this repo's own corpus that was 24
// closed blocks in a single transcript, none of which reached Atlas.
//
// Backfill is what an operator wants when the point of installing is to see the
// work that has already happened. It is safe to emit because it is PACED, not
// unbounded: maxPerSweep (24, the sidecar's own DEFAULT_MAX_BLOCKS) bounds one
// transcript's batch, the cursor advances only past contiguous successes, and a
// backlog therefore drains over successive sweeps instead of arriving as one
// burst on the wire.
func TestBackfillOnFirstSightEmitsHistory(t *testing.T) {
	dig := &fakeDig{
		all:       []enrich.BlockCharacterisation{block(1000), block(2200), block(6000)},
		watermark: f64(5000),
	}
	pub := &fakeSender{}
	e := newTestEmitter(t, dig, pub)
	e.backfill = true
	now := time.Unix(9000, 0)

	e.advanceAt("claude_code", txPath, now)
	if n := e.Sweep(context.Background(), now); n != 3 {
		t.Fatalf("backfill published %d rows, want all 3 historical blocks", n)
	}
	// It asked from the BEGINNING of the session (nil since) for a real batch,
	// rather than the seeding call's max_blocks 0.
	if c := dig.lastCall(); c.Since != nil || c.MaxBlocks != maxPerSweep {
		t.Fatalf("backfill call = %+v, want a nil since and max_blocks %d", c, maxPerSweep)
	}
	if got := pub.sent(); len(got) != 3 {
		t.Fatalf("published %v, want all three", got)
	}
}

// Backfill must still advance the cursor, so the SECOND sweep does not re-send
// the history it just sent. The cursor, not the toggle, is what makes backfill
// a one-time cost.
func TestBackfillDoesNotRepeatItself(t *testing.T) {
	dig := &fakeDig{
		all:       []enrich.BlockCharacterisation{block(1000), block(2200), block(6000)},
		watermark: f64(5000),
	}
	pub := &fakeSender{}
	e := newTestEmitter(t, dig, pub)
	e.backfill = true
	now := time.Unix(9000, 0)

	e.advanceAt("claude_code", txPath, now)
	e.Sweep(context.Background(), now)
	first := len(pub.sent())

	if n := e.Sweep(context.Background(), now); n != 0 {
		t.Fatalf("second sweep published %d rows, want 0 — the cursor must have advanced", n)
	}
	if len(pub.sent()) != first {
		t.Fatalf("second sweep re-sent history: %v", pub.sent())
	}
}

// With backfill OFF the documented forward-only seeding is unchanged. Pinned
// alongside so a future change to one branch cannot silently move the other.
func TestBackfillOffKeepsForwardOnlySeeding(t *testing.T) {
	dig := &fakeDig{
		all:       []enrich.BlockCharacterisation{block(1000), block(6000)},
		watermark: f64(5000),
	}
	pub := &fakeSender{}
	e := newTestEmitter(t, dig, pub)
	e.backfill = false
	now := time.Unix(9000, 0)

	e.advanceAt("claude_code", txPath, now)
	if n := e.Sweep(context.Background(), now); n != 0 {
		t.Fatalf("backfill off published %d rows on first sight, want 0", n)
	}
}
