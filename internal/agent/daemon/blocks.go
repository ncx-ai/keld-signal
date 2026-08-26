package daemon

import (
	"context"
	"log"
	"strings"
	"sync/atomic"

	"github.com/ncx-ai/keld-signal/internal/agent/blocks"
	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
)

// THE V2 BLOCK PATH's daemon wiring. The emitter itself is
// internal/agent/blocks; this file is only the three seams it needs — the
// publisher, the transcript's repository facts, and the watcher's advance
// signal — plus the switch that ships it OFF.
//
// It is a SEPARATE PATH from the tick, not a variant of it. tick.go stays
// exactly as it is: v2 must be promotable, or deletable, without unpicking v1.

// signalBlocksEndpoint derives the block ingest URL from the configured ingest
// endpoint by swapping the trailing path segment for /v1/signal/blocks — the
// same derivation every other route uses, and under the /v1/signal/* namespace
// new client<->Atlas routes go in (see docs/signal-client-events.md).
func signalBlocksEndpoint(ingest string) string {
	if i := strings.Index(ingest, "/v1/"); i >= 0 {
		return ingest[:i] + "/v1/signal/blocks"
	}
	return strings.TrimRight(ingest, "/") + "/v1/signal/blocks"
}

// startBlockEmitter wires and starts the v2 block path when it is switched on,
// and returns the advance observer the watcher's ingest signal feeds (nil when
// it is off, so the signal's call is a cheap nil check).
//
// The client-event on start is not decoration. Atlas can STORE a block —
// POST /v1/signal/blocks is built and answers 201 — but nothing reads one yet,
// and an operator who switched this on deserves to see that stated once per run
// rather than discover it by finding a table nothing joins. Emitted exempt from
// the severity floor for the same reason the lifecycle events are: it describes
// what this run WILL do.
//
// ingestEndpoint/token are taken rather than a ready-made *publish.Publisher
// because the block route is not the enrichments route: reusing the enrichment
// publisher would post blocks at /v1/enrichments, where the envelope is not
// even the same shape.
func startBlockEmitter(ctx context.Context, dig blocks.Digester, ingestEndpoint string,
	token func() string, actor string, emitter *clientevents.Emitter) func(source, path string) {
	if !blocks.Enabled() || dig == nil || token == nil {
		return nil
	}
	pub := publish.New(signalBlocksEndpoint(ingestEndpoint), token, actor)
	// One facts cache for the emitter's lifetime, shared across sweeps: a
	// transcript's checkout does not move between sweeps, and re-walking the
	// ReadDir chain plus .git/config for every active transcript every interval
	// is pure waste. Same cache the ticker and the ingest signal use.
	facts := newFactsCache()
	// NO PROMPT-ID READER: the emitter has no such seam any more. The `covers`
	// mapping it fed is deleted — a block is time end to end, and the time join
	// Atlas needs for cost attribution is the same one that answers which turns
	// ran inside a block. See the blocks package comment.
	em := blocks.New(dig, pub,
		func(path string) enrich.ResolvedFacts { return facts.forTranscript(path).resolved() },
		actor, blocks.StatePath())
	interval := blocks.IntervalFromEnv()
	log.Printf("keld-agent: v2 block emission ON (sweeping every %s). Blocks post to "+
		"/v1/signal/blocks; Atlas STORES them but nothing reads them yet.", interval)
	if emitter != nil {
		emitter.EmitExempt("blocks.emitter_enabled", clientevents.SevWarn, map[string]any{
			"interval_s":    int(interval.Seconds()),
			"read_at_atlas": false,
		})
	}
	go em.Run(ctx, interval)
	return em.Advance
}

// blockAdvance is how the watcher's per-file advance signal reaches the block
// emitter. A package-level atomic rather than another parameter threaded
// through the watcher wiring, mirroring tickObserver exactly: the emitter is
// optional, off by default, and needs nothing from the signal but its two
// coordinates, so widening the wiring to pass a usually-nil hook would cost
// more than it buys.
//
// It rides ingestSignalHook rather than a second WithIngestSignal, because the
// watcher takes ONE hook — and because the two want the identical scoping. A
// transcript the analysis cannot serve is exactly a transcript no block can be
// cut from, so enrich.WorkstreamsEligible gates both, in one place, and
// extending the analysis to a new source makes it eligible for both in the same
// edit.
var blockAdvance atomic.Pointer[func(source, path string)]

func setBlockAdvance(fn func(source, path string)) {
	if fn == nil {
		blockAdvance.Store(nil)
		return
	}
	blockAdvance.Store(&fn)
}

// noteBlockAdvance tells the block emitter a transcript grew. A no-op when the
// block path is off. Must stay cheap and non-blocking: it is called from the
// watcher's poll loop, the path every hook-free prompt on the machine travels.
func noteBlockAdvance(source, path string) {
	if fn := blockAdvance.Load(); fn != nil {
		(*fn)(source, path)
	}
}
