// Package features is THE SIGNAL-EMBEDDINGS PATH's daemon half: the emitter
// that asks the analysis service for the feature rows a transcript has
// produced, buffers them, and remembers how far it got.
//
//	a transcript -> reference series -> feature vector -> Atlas
//	at Keld:      (X at time t, y at time t+h) -> a model
//
// Design: docs/superpowers/specs/2026-08-26-signal-embeddings-design.md. This
// package is step 3 of four (Publish); steps 1 (Capture) and 2 (S(t)) are the
// sidecar's, and step 4 (Text) adds the encoder that fills the `text` half of a
// row this package already carries.
//
// # IT IS THE BLOCK EMITTER'S SIBLING, AND DIFFERS IN EXACTLY ONE PLACE
//
// The activity-driven active set, the settling sweep, the forward-only first
// sight, the monotonic per-transcript cursor and the atomic state file are all
// internal/agent/blocks' shape, for its reasons: a machine has MANY transcripts
// — 496 in the reference corpus — and an emitter that polled every known one
// every interval would do 496 sidecar round-trips per interval forever,
// essentially all returning nothing. That does not hold at any interval worth
// having. Work per interval is proportional to ACTIVE transcripts (one or two
// on a real machine), and the settling sweep is what buys the trailing rows a
// purely activity-driven emitter would never collect.
//
// ⚠️ THE ONE DIFFERENCE IS THE DURABILITY BOUNDARY, and it is forced by volume.
// The block emitter publishes INLINE and holds its cursor until a POST
// succeeds, with no spool at all — safe because a block is re-derivable from
// the store for 400 days, so an unpublished block is still there weeks later.
// This path cannot do that: ~200 KB per user per active day across ~190 rows is
// an order more than any existing row type, so it MUST batch, and once it
// batches "delivered" is not something the sweep can observe. So rows are
// handed to a bounded buffer, the cursor advances on that handoff, and
// durability from there is the transport's spool (internal/agent/clientevents'
// Transport — bounded, internal/retry, drop-oldest under ~/.keld/spool).
//
// What makes that safe is BACKPRESSURE rather than optimism: a sweep never
// takes more rows than the buffer has room for, so the buffer cannot overflow
// and the cursor can never advance past a row dropped for space. A full buffer
// stops the sweep taking anything, which holds the cursor — the same
// queue-rather-than-degrade rule the enrichment path follows.
//
// # WHAT CROSSES TO THE SIDECAR
//
// A transcript PATH, a cursor instant, the clock, a batch bound, and the
// repository facts the sidecar is confined out of reading. Nothing else — no
// text, no ids, no offsets. And nothing text-shaped comes BACK: what the
// sidecar answers is an int8-quantised array, a scale, and identities drawn
// from closed vocabularies, every one of them gated at the decode boundary
// (see sidecar.FeatureRowsFor's six refusals).
package features

import (
	"context"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// Source is the capability the emitter needs from the analysis service (the
// sidecar's POST /features): "which feature rows has this transcript produced
// since the cursor". An interface rather than a *sidecar.Client so the emitter
// is testable without a live sidecar, mirroring how the daemon declares
// windowAnalyzer / piiDetector / windowTicker / blocks.Digester.
//
// The route enumerates the anchors itself and takes only a cursor, because the
// sidecar owns the store and is the only side that can see where the non-empty
// bins and the closed blocks are. See internal/agent/enrich/sidecar/features.go
// for the full contract and for the four response keys this side deliberately
// does not model.
type Source interface {
	FeatureRowsFor(path, source, sessionID string,
		since *float64, now time.Time, maxRows int,
		resolved enrich.ResolvedFacts) ([]enrich.FeatureVector, *float64, bool)
}

// Facts resolves the daemon-side repository facts for a transcript. The sidecar
// is confined out of reading a repo's .git/config, so a vector whose `repo`
// level could not be named is a lesser vector for no reason. May be nil, which
// sends empty facts.
type Facts func(transcriptPath string) enrich.ResolvedFacts

// idleSeconds mirrors the sidecar's own idle threshold — blocks.IDLE_BINS (3) x
// BIN_SECONDS (300) = 900s — and is used for ONE thing on this side: deciding
// when a transcript has settled enough to leave the active set. The sidecar
// remains the only party that decides which rows are emittable; this side never
// re-implements that rule, it only asks again until the answer stops changing.
const idleSeconds = 15 * time.Minute

// settleGrace is added to idleSeconds before a transcript may leave the active
// set. The retirement test is "a sweep returned no rows and the last advance is
// older than the idle threshold", and evaluating that at exactly the threshold
// races the sidecar's own settle: a trailing block's row becomes emittable at
// `end + IDLE`, and `end` is the last active BIN's end, which is after the last
// turn the watcher saw. Retiring one sweep too early would drop the transcript
// in the interval before its final rows appear, and nothing would put it back —
// the session is over, so no further advance signal is coming. One bin of grace
// closes that.
const settleGrace = 5 * time.Minute

// maxPerSweep bounds one transcript's take from a single call.
//
// Sized against the anchor cadences rather than picked: a five-minute interval
// spans one `bin` row, at most one `block` row, and however many messages the
// person and the agent exchanged in it. 64 covers a dense five minutes several
// times over, and a genuine backlog — a machine that was off, a session
// replayed — drains over successive sweeps, which the cursor is what makes
// safe.
const maxPerSweep = 64

// bufferCapacity is how many rows may wait in memory for the reporter.
//
// ⚠️ IT IS A MEMORY BOUND EXPRESSED IN ROWS, and the arithmetic matters because
// these rows are big. A structured row is ~1.4 KB quantised and a message row
// ~0.8 KB, so 512 rows is roughly 700 KB held — and it is about 2.7 days of one
// user's production (~190 rows per active day), which is the right order for
// "Atlas was unreachable over a weekend" without being the right order for
// "hold a month". Past that the SWEEP STOPS TAKING rather than the buffer
// dropping: see the package comment on backpressure.
const bufferCapacity = 512

// batchRows is how many rows go in one POST.
//
// ⚠️ IT IS A DURABILITY PARAMETER, NOT A THROUGHPUT ONE, the same call
// blocks.batchSize makes one path over. One POST is one all-or-nothing outcome
// and one spool file, so the batch size is exactly the granularity at which
// progress is kept when the network drops mid-drain: chunking a 512-row backlog
// into eight POSTs means a failure on the second still banks the first 64. It
// is also a body-size bound — 64 structured rows is ~90 KB of JSON, which is a
// request an ingest endpoint can be handed without special arrangements.
const batchRows = 64

// Emitter is the feature path's daemon-side collector. Zero value is not
// usable; see New.
type Emitter struct {
	src   Source
	facts Facts
	actor string
	// gate is read PER SWEEP, not captured at startup. The org's override
	// arrives on the first settings poll, which lands after wiring, so binding
	// the toggle at construction would ignore the org until the daemon
	// restarted — the one thing the local-then-remote shaping exists to avoid.
	// The same reason facetsFor resolves PII regions per call. Nil reads as on,
	// which is what the tests and the eval want.
	gate func() bool
	st   *state

	mu sync.Mutex
	// active is the bounded set of transcripts that might still have a row to
	// collect. See the package comment: this is what keeps work proportional to
	// ACTIVE transcripts rather than to the hundreds a machine accumulates.
	active map[string]bool
	// buf is the bounded hand-off to the reporter. Drop-oldest is deliberately
	// NOT implemented here — the sweep refuses to take rows it has no room for,
	// so a row that reached this slice is a row the cursor was allowed to move
	// past.
	buf []publish.FeatureRow
}

// New builds an emitter over the given state file. facts may be nil in tests,
// which sends empty resolved facts; gate may be nil, which reads as on.
func New(src Source, facts Facts, gate func() bool, actor, stateFile string) *Emitter {
	return &Emitter{
		src: src, facts: facts, gate: gate, actor: actor,
		st: newState(stateFile), active: map[string]bool{},
		buf: make([]publish.FeatureRow, 0, bufferCapacity),
	}
}

// StatePath is where the emitter's cursors live. Beside the block emitter's
// blocks.json and the tick's tick.json, and deliberately not in either: the
// three paths have different cursors with different meanings, and one file
// would couple their lifetimes.
func StatePath() string { return filepath.Join(paths.StateDir(), "features.json") }

// on reports whether the subsystem is switched on right now.
func (e *Emitter) on() bool { return e.gate == nil || e.gate() }

// Advance is the watcher's per-file signal that a transcript grew, in the shape
// watch.WithIngestSignal hands out. It is the emitter's ONLY trigger for adding
// work: a transcript nothing has written to cannot have produced a row that has
// not already settled and been collected.
//
// Cheap and non-blocking by construction — a map write under a short mutex —
// because it is called from the watcher's poll loop, which is the path every
// hook-free prompt on the machine travels. Nothing here does I/O, and nothing
// here waits on the sidecar.
func (e *Emitter) Advance(source, path string) { e.advanceAt(source, path, time.Now()) }

// advanceAt is Advance with the clock injected, so a test can exercise the
// settling rule — which is a statement about elapsed time — without sleeping.
func (e *Emitter) advanceAt(source, path string, now time.Time) {
	if path == "" || !e.on() {
		return
	}
	e.st.note(source, sessionIDFor(path), path, now)
	e.mu.Lock()
	e.active[path] = true
	e.mu.Unlock()
}

// activePaths snapshots the active set.
func (e *Emitter) activePaths() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, 0, len(e.active))
	for p := range e.active {
		out = append(out, p)
	}
	return out
}

// retire removes a transcript from the active set. Its cursor is KEPT — the
// transcript is settled, not gone, and work can resume on it tomorrow.
func (e *Emitter) retire(path string) {
	e.mu.Lock()
	delete(e.active, path)
	e.mu.Unlock()
}

// ActiveCount reports how many transcripts are in the active set. For tests and
// diagnostics: the whole scale argument is that this stays small.
func (e *Emitter) ActiveCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.active)
}

// Buffered reports how many rows are waiting for the reporter.
func (e *Emitter) Buffered() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.buf)
}

// room is how many more rows the buffer will accept. See the package comment:
// this is the backpressure that makes advancing the cursor on buffering safe.
func (e *Emitter) room() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return bufferCapacity - len(e.buf)
}

// Drain hands the buffered rows to the reporter and empties the buffer. The
// returned slice is the caller's; the emitter never writes to it again.
func (e *Emitter) Drain() []publish.FeatureRow {
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.buf) == 0 {
		return nil
	}
	out := e.buf
	e.buf = make([]publish.FeatureRow, 0, bufferCapacity)
	return out
}

// Run drives the emitter until ctx ends.
func (e *Emitter) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.Sweep(ctx, time.Now())
		}
	}
}

// Sweep is one pass over the active set. Split out so a test can drive it with
// a fixed clock rather than waiting on a timer. Returns how many rows were
// buffered.
func (e *Emitter) Sweep(ctx context.Context, now time.Time) int {
	if !e.on() {
		// Switched off mid-run (an org flipped it, or it was never on). Take
		// nothing and hold every cursor: the rows are still in the store, so
		// turning it back on resumes rather than backfills.
		return 0
	}
	buffered := 0
	for _, tgt := range e.st.targets(e.activePaths()) {
		if ctx.Err() != nil {
			return buffered
		}
		buffered += e.sweepOne(tgt, now)
	}
	active := map[string]bool{}
	for _, p := range e.activePaths() {
		active[p] = true
	}
	e.st.prune(active, now)
	if err := e.st.save(); err != nil {
		log.Printf("keld-agent: feature emitter state not persisted: %v", err)
	}
	return buffered
}

// sweepOne handles one transcript.
//
// FIRST SIGHT IS FORWARD-ONLY, and it costs one call that collects nothing. The
// sidecar reads a nil `since_ts` as "from the beginning of the session", i.e.
// BACKFILL — so on a transcript with no cursor the emitter asks for the
// WATERMARK and no rows (max_rows 0), seeds the cursor there, and collects on
// the next sweep. That matches KELD_WATCH_BACKFILL's forward-only default and
// is what stops a daemon restart, or a first install on a machine with 496
// transcripts, from producing a herd of history — which here would mean an
// encoder forward pass per message of every one of them.
//
// A transcript whose store has never been ingested has a nil watermark. It is
// left UNSEEDED rather than seeded at zero: a zero cursor is a real instant in
// 1970 and would backfill everything. The next sweep asks again.
func (e *Emitter) sweepOne(tgt target, now time.Time) int {
	var resolved enrich.ResolvedFacts
	if e.facts != nil {
		resolved = e.facts(tgt.Path)
	}

	if tgt.Cursor == nil {
		_, watermark, ok := e.src.FeatureRowsFor(tgt.Path, tgt.Source, tgt.Session,
			nil, now, 0, resolved)
		if ok && watermark != nil {
			e.st.advance(tgt.Path, *watermark)
		}
		return 0
	}

	// BACKPRESSURE: never ask for more than the buffer will hold. A full buffer
	// takes nothing at all, which holds the cursor and re-offers the same
	// ground next interval — the queue-rather-than-degrade rule, applied to
	// memory instead of to a missing model.
	want := e.room()
	if want <= 0 {
		return 0
	}
	if want > maxPerSweep {
		want = maxPerSweep
	}

	rows, _, ok := e.src.FeatureRowsFor(tgt.Path, tgt.Source, tgt.Session,
		tgt.Cursor, now, want, resolved)
	if !ok {
		// The sidecar could not answer (not ready, restarting, store behind, or
		// — until step 2 lands — the route does not exist). Do not advance and
		// do not retire: the next sweep asks for the same ground, which is
		// exactly what a held cursor buys.
		return 0
	}

	if len(rows) == 0 {
		// SETTLED? Nothing new, and the transcript has been quiet longer than
		// the idle threshold the trailing rows settle on. Anything still to
		// come would need new activity, and new activity puts it back here.
		if e.st.idleSince(tgt.Path, now) >= idleSeconds+settleGrace {
			e.retire(tgt.Path)
		}
		return 0
	}
	return e.buffer(tgt, rows, now)
}

// buffer converts the rows and appends what fits, advancing the cursor to the
// last row ACCEPTED.
//
// ⚠️ THE CURSOR STOPS AT THE LAST ACCEPTED ROW, NOT THE LAST OFFERED ONE. The
// backpressure above makes a short accept nearly impossible, but "nearly" is
// not a durability argument: if the buffer filled between the room() read and
// here (a concurrent Advance cannot do it, but a future second producer could),
// advancing past a row that was not taken would lose it permanently, since the
// sidecar answers `since_ts` and would never offer it again. Stopping at the
// contiguous frontier of what landed is the same rule the block emitter applies
// to what was POSTED.
func (e *Emitter) buffer(tgt target, rows []enrich.FeatureVector, now time.Time) int {
	e.mu.Lock()
	accepted := 0
	var last float64
	for _, v := range rows {
		if len(e.buf) >= bufferCapacity {
			break
		}
		e.buf = append(e.buf, publish.BuildFeature(v, e.actor, now))
		last = v.TSUnix
		accepted++
	}
	e.mu.Unlock()

	if accepted > 0 {
		e.st.advance(tgt.Path, last)
	}
	return accepted
}

// sessionIDFor is the session identifier a feature row publishes: the
// transcript's file stem.
//
// NOT the reference series' own key, which is a digest of the absolute path
// (sidecar app/analysis/ingest.py's session_of) and is machine-local by
// construction — it joins to nothing downstream. For Claude Code the stem IS
// the session uuid, which is what Atlas can key a session on.
func sessionIDFor(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}
