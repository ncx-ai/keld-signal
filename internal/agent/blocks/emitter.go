// Package blocks is THE V2 PATH's daemon half: the emitter that asks the
// sidecar which CLOSED blocks of work a transcript has, publishes each one, and
// remembers how far it got.
//
//	a transcript -> blocks of work -> one characterisation per block
//
// No prompt anchor, no look-back window, no gap-filling. Blocks TILE ACTIVE
// TIME, so coverage is 100% of activity by construction and there are no holes
// to patch. Design:
// docs/superpowers/specs/2026-08-25-v2-block-path-design.md.
//
// # IT IS NOT THE TICK AND MUST NOT BE BUILT ON IT
//
// The tick exists to patch the holes a prompt-anchored 60-minute window leaves,
// and its whole difficulty is the FRONTIER: it must reason about which FUTURE
// prompts might sweep backwards over a moment, because a prompt's window
// reaches back an hour. A block reaches nowhere. It is a span with a determined
// end, and nothing later can overlap it, so the frontier problem does not exist
// here and `coverage.frontier` / `tail_closed` must not be ported in. The tick
// is v1 and is slated for removal once this path is read at Atlas.
//
// # WHY THE EMITTER IS DRIVEN BY ACTIVITY, WITH A SETTLING SWEEP
//
// ⚠️ THIS IS THE SHAPE-DETERMINING CONSTRAINT, not a tuning detail. A machine
// has MANY transcripts — 496 in the reference corpus — and an emitter that
// polled every known one every interval would do 496 sidecar round-trips per
// interval forever, essentially all of them returning nothing. That does not
// hold at any interval worth having.
//
// Two facts are in tension:
//
//   - A block CLOSES CONTINUOUSLY DURING a session. A `budget` or `idle` cut is
//     final the moment it is reached, because later activity starts a NEW block
//     and cannot alter this one. So activity is the natural trigger, and a
//     working day must not produce its first block only once the person stops.
//   - The TRAILING block closes one IDLE THRESHOLD AFTER activity stops. Its
//     end is "where the data currently stops" rather than a real boundary, and
//     only silence settles it. A purely activity-driven emitter would therefore
//     never emit the last block of any session.
//
// So the emitter keeps a bounded ACTIVE SET of transcripts that might still
// have an unsettled block: a transcript joins on the watcher's advance signal,
// and LEAVES once a sweep returns no blocks AND its last advance is older than
// the idle threshold. Work per interval is proportional to ACTIVE transcripts —
// one or two on a real machine — not to known ones. The settling sweep is what
// buys the trailing block; the advance signal is what keeps the cost bounded.
//
// # WHAT CROSSES TO THE SIDECAR
//
// A transcript PATH, a cursor instant, the clock, a batch bound, and the
// repository facts the sidecar is confined out of reading. Nothing else — no
// text, and (since the `covers` mapping was deleted) NO IDS EITHER.
//
// ⚠️ Do not re-add a prompt-id list here. A block is TIME end to end: cutting
// is a cap plus a silence threshold, the digest is a rollup over a span, and the
// principal comes from the device's own auth. Atlas must join
// `event_ts ∈ [block.start, block.end)` within the session for cost attribution
// regardless — a turn spanning several blocks would double-count its spend
// through a prompt mapping — and that same join answers the display question,
// because Atlas holds ToolEvent.session_id and event_ts. The mapping was
// therefore a second, weaker copy of a join Atlas owes anyway; it also never
// worked, since watch/filter.go names prompts by `promptId` while the sidecar's
// store indexes the per-message `uuid`, so every real block published an empty
// list.
package blocks

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// Digester is the capability the emitter needs from the analysis service (the
// sidecar's POST /blocks): "which closed blocks does this transcript have, and
// what was each one". An interface rather than a *sidecar.Client so the emitter
// is testable without a live sidecar, mirroring how the daemon declares
// windowAnalyzer / piiDetector / windowTicker.
type Digester interface {
	BlocksCharacterised(path, source, sessionID string,
		since *float64, now time.Time, maxBlocks int,
		resolved enrich.ResolvedFacts) ([]enrich.BlockCharacterisation, *float64, bool)
}

// Sender publishes a batch of block rows. Separate from the enrichment
// publisher's Send because a block is a different wire shape on a different
// route — see publish.BlockEnrichment for why that distinction is structural
// rather than tidy.
type Sender interface {
	SendBlocks([]publish.BlockEnrichment) error
}

// Facts resolves the daemon-side repository facts for a transcript. The sidecar
// is confined out of reading a repo's .git/config, so a block that could not
// name its repository is a lesser block for no reason. May be nil, which sends
// empty facts.
type Facts func(transcriptPath string) enrich.ResolvedFacts

// idleSeconds mirrors the sidecar's blockdigest.IDLE_SECONDS — blocks.IDLE_BINS
// (3) x BIN_SECONDS (300) = 900s — and is used for ONE thing on this side:
// deciding when a transcript has settled enough to leave the active set. The
// sidecar remains the only party that decides whether a block is CLOSED; this
// side never re-implements that rule, it only asks again until the answer stops
// changing.
const idleSeconds = 15 * time.Minute

// settleGrace is added to idleSeconds before a transcript may leave the active
// set. The retirement test is "a sweep returned no blocks and the last advance
// is older than the idle threshold", and evaluating that at exactly the
// threshold races the sidecar's own settle: the trailing block becomes
// emittable at `end + IDLE`, and `end` is the last active BIN'S end, which is
// after the last turn the watcher saw. Retiring one sweep too early would drop
// the transcript in the interval before its final block appears, and nothing
// would put it back — the session is over, so no further advance signal is
// coming. One bin of grace closes that.
const settleGrace = 5 * time.Minute

// maxPerSweep bounds one transcript's batch, matching the sidecar's own
// DEFAULT_MAX_BLOCKS. Stated here too because the daemon is the party that
// would feel an unbounded batch, as a burst of rows on the wire. A backlog
// drains over successive sweeps; the cursor is what makes that safe.
const maxPerSweep = 24

// batchSize is how many blocks go in one POST.
//
// ⚠️ IT IS A DURABILITY PARAMETER, NOT A THROUGHPUT ONE. The cursor may only
// advance past blocks whose publish SUCCEEDED, and one POST is one all-or-
// nothing outcome — so the batch size is exactly the granularity at which
// progress can be kept when the network drops mid-drain. Chunking a 24-block
// backlog into three POSTs means a failure on the second still banks the first
// eight; sending all 24 at once would re-do the whole drain next interval. It
// is small because re-doing work is free (a block's identity is deterministic
// and Atlas upserts) but LOSING progress across a flaky link is not.
const batchSize = 8

// Emitter is the block path's daemon-side loop. Zero value is not usable; see
// New.
type Emitter struct {
	dig   Digester
	pub   Sender
	facts Facts
	actor string
	st    *state

	mu sync.Mutex
	// active is the bounded set of transcripts that might still have an
	// unsettled block. See the package comment: this is what keeps work
	// proportional to ACTIVE transcripts rather than to the hundreds a machine
	// accumulates.
	active map[string]bool
}

// New builds an emitter over the given state file. facts may be nil in tests,
// which sends empty resolved facts.
//
// There is deliberately no prompt-id reader parameter: see the package comment
// on why the `covers` mapping that needed one was deleted rather than fixed.
func New(dig Digester, pub Sender, facts Facts, actor, stateFile string) *Emitter {
	return &Emitter{
		dig: dig, pub: pub, facts: facts, actor: actor,
		st: newState(stateFile), active: map[string]bool{},
	}
}

// StatePath is where the emitter's cursors live. Beside the tick's tick.json
// and deliberately NOT in it: the two paths have different cursors with
// different meanings, and one file would couple v2's lifetime to v1's.
func StatePath() string { return filepath.Join(paths.StateDir(), "blocks.json") }

// Advance is the watcher's per-file signal that a transcript grew, in the shape
// watch.WithIngestSignal hands out. It is the emitter's ONLY trigger for adding
// work: a transcript nothing has written to cannot have a block that has not
// already settled and been emitted.
//
// Cheap and non-blocking by construction — a map write under a short mutex —
// because it is called from the watcher's poll loop, which is the path every
// hook-free prompt on the machine travels. Nothing here does I/O.
func (e *Emitter) Advance(source, path string) { e.advanceAt(source, path, time.Now()) }

// advanceAt is Advance with the clock injected, so a test can exercise the
// settling rule — which is a statement about elapsed time — without sleeping.
func (e *Emitter) advanceAt(source, path string, now time.Time) {
	if path == "" {
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
// a fixed clock rather than waiting on a timer. Returns how many block rows
// were published.
func (e *Emitter) Sweep(ctx context.Context, now time.Time) int {
	published := 0
	activeBefore := e.activePaths()
	for _, tgt := range e.st.targets(activeBefore) {
		if ctx.Err() != nil {
			return published
		}
		published += e.sweepOne(tgt, now)
	}
	active := map[string]bool{}
	for _, p := range e.activePaths() {
		active[p] = true
	}
	e.st.prune(active, now)
	if err := e.st.save(); err != nil {
		log.Printf("keld-agent: block emitter state not persisted: %v", err)
	}
	return published
}

// sweepOne handles one transcript.
//
// FIRST SIGHT IS FORWARD-ONLY, and it costs one call that emits nothing. The
// sidecar reads a nil `since_ts` as "from the beginning of the session", i.e.
// BACKFILL — so on a transcript with no cursor the emitter asks for the
// WATERMARK and no blocks (max_blocks 0), seeds the cursor there, and emits on
// the next sweep. That matches KELD_WATCH_BACKFILL's
// forward-only default and is what stops a daemon restart, or a first install
// on a machine with 496 transcripts, from emitting a herd of history.
//
// ⚠️ THE STATED COST: the block the transcript was in the middle of at first
// sight starts before the watermark, so it is excluded and never emitted. One
// block per transcript, once. The alternative — seeding below the watermark —
// has no safe floor: any margin large enough to catch that block is also large
// enough to backfill on a transcript that has been running all day.
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
		_, watermark, ok := e.dig.BlocksCharacterised(tgt.Path, tgt.Source, tgt.Session,
			nil, now, 0, resolved)
		if ok && watermark != nil {
			e.st.advance(tgt.Path, *watermark)
		}
		return 0
	}

	blocks, _, ok := e.dig.BlocksCharacterised(tgt.Path, tgt.Source, tgt.Session,
		tgt.Cursor, now, maxPerSweep, resolved)
	if !ok {
		// The sidecar could not answer (not ready, restarting, store behind).
		// Do not advance and do not retire: the next sweep asks for the same
		// ground, which is exactly what a held cursor buys. Queue rather than
		// degrade, the same rule the enrichment path follows.
		return 0
	}

	if len(blocks) == 0 {
		// SETTLED? Nothing closed, and the transcript has been quiet longer than
		// the idle threshold the trailing block settles on. Anything still to
		// come would need new activity, and new activity puts it back here.
		if e.st.idleSince(tgt.Path, now) >= idleSeconds+settleGrace {
			e.retire(tgt.Path)
		}
		return 0
	}
	return e.publish(tgt, blocks, now)
}

// publish sends the blocks in order and advances the cursor to the end of the
// last CONTIGUOUS success.
//
// ⚠️ THE CURSOR IS A WATERMARK, NOT A SET, AND THIS IS THE SILENT-DATA-LOSS
// RULE OF THE WHOLE PATH. Clients are not reliably online: a laptop suspends, a
// link drops mid-drain, Atlas has an outage. The obvious implementation —
// fetch, publish, advance — loses every block of a failed batch FOREVER,
// because the sidecar answers `since_ts` and will never return them again. So:
//
//   - The cursor advances only past blocks whose publish SUCCEEDED.
//   - On failure it stops at the last contiguous success and STAYS THERE. It
//     never advances past a gap, even if a later chunk would have succeeded,
//     because a watermark cannot express a hole.
//   - Recovery is re-fetching and re-publishing, which is FREE: a block's
//     identity is `(session, block.start)` and Atlas upserts, so a re-delivered
//     block is not a duplicate. That is deliberately preferred over tracking
//     which individual blocks landed — per-row delivery state is a second
//     durable thing to get wrong, and the whole point of a deterministic
//     identity is not needing one.
//
// THERE IS NO SPOOL HERE, and that is a decision rather than an omission. The
// client-events path spools because its events are ephemeral — nothing can
// regenerate a dropped one. A block can always be regenerated: the reference
// series retains events for 400 days, so an unpublished block is still fully
// derivable from the store weeks later, and the held cursor is what asks for it
// again. Spooling would be a second copy of data that already exists on disk,
// with its own eviction policy to get wrong. Cursor-hold is the correctness
// floor and here it is also the ceiling.
func (e *Emitter) publish(tgt target, blocks []enrich.BlockCharacterisation, now time.Time) int {
	sent := 0
	for start := 0; start < len(blocks); start += batchSize {
		end := start + batchSize
		if end > len(blocks) {
			end = len(blocks)
		}
		chunk := blocks[start:end]
		rows := make([]publish.BlockEnrichment, 0, len(chunk))
		for _, b := range chunk {
			rows = append(rows, publish.BuildBlock(b, e.actor, now))
		}
		if err := e.pub.SendBlocks(rows); err != nil {
			log.Printf("keld-agent: block publish failed for %s: %v", tgt.Session, err)
			// Stop here. Everything before this chunk is banked by the advances
			// below; everything from this chunk on is re-fetched next interval.
			return sent
		}
		sent += len(chunk)
		// Advance only after the batch is CONFIRMED, and to the last block of
		// that batch — the contiguous frontier of what landed.
		e.st.advance(tgt.Path, chunk[len(chunk)-1].EndTS)
	}
	return sent
}

// unixSeconds is an instant in the form every seam on this path speaks — epoch
// seconds as a float, the same shape as the cursor and as a block's
// StartTS/EndTS. Sub-second precision is kept: a range edge is compared against
// record timestamps that carry milliseconds.
func unixSeconds(t time.Time) float64 { return float64(t.UnixNano()) / 1e9 }

// sessionIDFor is the session identifier a block row publishes: the
// transcript's file stem.
//
// NOT the reference series' own key, which is a digest of the absolute path
// (sidecar app/analysis/ingest.py's session_of) and is machine-local by
// construction — it joins to nothing downstream. For Claude Code the stem IS
// the session uuid, which is what Atlas can key a session on.
func sessionIDFor(path string) string {
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}

// EnvEnabled is the switch that turns the v2 block path on, and it ships OFF
// for a stated reason rather than out of caution.
//
// Atlas can STORE blocks — POST /v1/signal/blocks is built and returns 201 —
// but nothing reads them yet. Emitting rows into a table no consumer joins is
// the same shape the tick already ships in, and the rule this repo holds to is
// that such rows are opt-in and announced rather than quietly accumulated.
// Flipping the default is a one-line change the day Atlas reads them.
//
// ⚠️ The COMPILED-IN default is still off and this work did not change it. What
// changed is that a v2 installer now writes `"blocks": true` per machine (see
// docs/superpowers/specs/2026-08-27-v2-installer-design.md), so an installed
// machine emits blocks while a `go run` / CI / eval machine does not.
const EnvEnabled = "KELD_BLOCKS"

// EnvInterval overrides the sweep interval.
const EnvInterval = "KELD_BLOCKS_INTERVAL"

// DefaultInterval is the sweep cadence, and it is PURE LATENCY: a block is
// emitted at most one interval after it closes, and the blocks themselves are
// identical whatever the interval, because the sidecar decides closure from the
// store and the clock, not from when it was asked.
//
// Five minutes, for a structural reason rather than a preference. Blocks are
// cut on the reference series' own 5-minute bins (BIN_SECONDS = 300) and a
// block's edges ARE bin edges, so nothing can close at a finer granularity than
// that: a shorter interval buys no freshness at all, only repeated queries that
// return what the last one did. The resulting latencies are:
//
//	mid-session block   <= 5 min after it closes (it closes as work moves past)
//	trailing block      <= 20 min (the 15-minute idle settle + one interval)
//
// Cost is one /blocks call per ACTIVE transcript per interval — one or two on a
// real machine, and a call is a series query measured in single-digit
// milliseconds. It is emphatically NOT one call per KNOWN transcript; see the
// package comment on why that distinction is the whole design.
const DefaultInterval = 5 * time.Minute

// Enabled reports the LOCAL value of the `blocks` toggle: KELD_BLOCKS, else the
// agent-config value the caller resolved, else off.
//
// It takes the config value rather than reading agent-config.json itself for the
// reason features.Enabled does: the precedence is stated in one place, and this
// package does not import settings.
//
// ⚠️ The config key exists because an env-only toggle is UNREACHABLE FROM AN
// INSTALLER. No service definition on any OS carries an environment block —
// LaunchAgentPlist and SystemdUnit have none, and the Windows scheduled task is a
// bare /TR "<exe>" run — so there is nowhere an installer could put KELD_BLOCKS
// that the daemon would ever see. The v2 installer writes `"blocks": true` into
// agent-config.json instead.
//
// The env variable still WINS, in both directions, so KELD_BLOCKS=0 switches off
// a machine the installer switched on without editing JSON.
func Enabled(fromConfig bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvEnabled))) {
	case "1", "true", "on", "yes":
		return true
	case "0", "false", "off", "no":
		return false
	}
	return fromConfig
}

// IntervalFromEnv is the sweep interval, DefaultInterval unless overridden.
func IntervalFromEnv() time.Duration {
	if v := os.Getenv(EnvInterval); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return DefaultInterval
}
