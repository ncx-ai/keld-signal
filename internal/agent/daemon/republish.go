package daemon

// republish.go — deliverable B2 (Send to Atlas, docs/v3/contracts.md, T38):
// when a machine that ran with Send to Atlas OFF is later turned back on, the
// blocks it cut while off must reach Atlas without being re-cut. The block
// emitter itself only ever hands rows to whatever Sender it was built with
// (see daemon/blocks.go): with Atlas off that Sender is localOnlySender,
// which discards and reports success (so the emitter's cursor advances and
// never re-offers the block) — by design, so a deliberately offline machine
// never spools work it will never send. That design is exactly what makes a
// SEPARATE recovery path necessary once Atlas comes back on: nothing else
// remembers what was discarded.
//
// The ledger's own `blocks` table (store.go) does not hold enough to rebuild
// the wire shape (publish.BlockEnrichment) — it keeps three workstream dims
// and a handful of delivery-page cells, never the full workstreams/dynamics/
// effort/inventory facets a real block carries. So this file captures the
// block's own marshalled JSON at CUT time (ledger.Store.SaveUnsentPayload, a
// new table — see ledger/unsent.go) and republishes EXACTLY that payload:
// nothing is re-cut and nothing is re-characterised.
//
// ⚠️ **THE SWEEP USED TO RUN ONCE PER DAEMON START AND GIVE UP FOR GOOD.** Five
// attempts inside that single sweep, then `return`, and nothing re-entered the
// function until the process restarted. Measured on a real machine: a
// TRANSIENT 422 from Atlas exhausted those five attempts, 41 captured blocks
// stayed captured, and — because this sweep is the ONLY thing in a running
// daemon that calls atlas.Client.SendBlocks (the live block emitter posts
// through its own publish.Publisher, which never notes a response back to the
// Atlas connector) — `atlas.Live.LastResponse` stayed pinned at that 422 and
// the health strip's `atlas` row read "batch refused" for days. Re-posting all
// 41 payloads by hand against the same Atlas returned 201/stored on every one:
// the payloads were fine and the refusal was long gone, with nothing on the
// machine able to notice.
//
// So the sweep now RECURS, with three consequences that are the whole of this
// change:
//   - A later success is what clears the strip, and it clears itself: a
//     successful SendBlocks updates LastResponse, startHealth re-reads it every
//     10s, and the ledger's health row upserts by key. Nothing here writes a
//     health row and nothing here fakes one — if the last real outcome was a
//     rejection and nothing has been tried since, the strip still says refused,
//     which is correct.
//   - Recurring must not mean hammering. The gap between sweeps DOUBLES while
//     nothing is landing (republishInterval → republishMaxInterval) and resets
//     the moment anything does.
//   - Recurring must not mean retrying a genuine rejection forever. Each
//     payload's solo refusals are counted in the ledger and it is held aside at
//     ledger.UnsentRefusalLimit — kept, still marked refused on its own block
//     row, never offered again.

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/atlas"
)

// republishBatchSize mirrors the block emitter's own batchSize
// (internal/agent/blocks/emitter.go): the granularity at which progress is
// kept if the network drops mid-drain, not a throughput knob.
const republishBatchSize = 8

// The sweep cadence. Vars, not consts, so a test does not have to spend real
// minutes proving a later sweep happens.
//
// ⚠️ **THE BACKOFF IS LOAD-BEARING FOR THE REFUSAL BOUND, not just politeness
// toward Atlas.** ledger.UnsentRefusalLimit (5) is what stops a refused
// payload being retried forever, and five attempts five minutes apart is 20
// minutes — short enough that a server-side wobble lasting an afternoon would
// hold every captured block aside permanently. Doubling spreads those five
// attempts over 0 + 5 + 10 + 20 + 40 minutes, so the bar for calling a refusal
// genuine becomes "Atlas has refused this block, on its own, across an hour
// and a quarter". Raising the limit and shortening the interval would buy the
// same wall clock for more requests; this way costs fewer.
var (
	republishInterval    = 5 * time.Minute
	republishMaxInterval = time.Hour
)

// captureOnCut wraps a block emitter's OnCut hook (see blocks.Emitter.OnCut,
// fired for every block the sweep BUILT, before any publish is attempted) so
// each block's own characterisation survives local-only mode discarding it.
// next is called first and its panics are isolated exactly like
// chainOnPublished does one hook over — a capture failure must never cost the
// ledger's own recording, and neither may cost the sweep.
func captureOnCut(store *ledger.Store, next func(rows []publish.BlockEnrichment, path string)) func([]publish.BlockEnrichment, string) {
	return func(rows []publish.BlockEnrichment, path string) {
		if next != nil {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("keld-agent: recordCut panicked, ignoring: %v", r)
					}
				}()
				next(rows, path)
			}()
		}
		saveUnsentPayloads(store, rows)
	}
}

func saveUnsentPayloads(store *ledger.Store, rows []publish.BlockEnrichment) {
	if store == nil {
		return
	}
	now := time.Now().UTC()
	for _, r := range rows {
		k, ok := blockKeyOf(r)
		if !ok {
			continue
		}
		b, err := json.Marshal(r)
		if err != nil {
			continue
		}
		store.SaveUnsentPayload(k, b, now)
	}
}

// blockKeyOf reuses epochOf (v3blocks.go) so the key this file computes can
// never drift from the one recordCut/recordDelivered already use for the
// SAME row.
func blockKeyOf(r publish.BlockEnrichment) (ledger.BlockKey, bool) {
	start, ok := epochOf(r.Window.Start)
	if !ok || r.SessionID == "" {
		return ledger.BlockKey{}, false
	}
	return ledger.BlockKey{Session: r.SessionID, Start: start}, true
}

// sweepOutcome is what one sweep concluded, and it is the loop's whole policy.
type sweepOutcome int

const (
	// sweepDone: nothing remains that this daemon will ever offer again —
	// either the table is empty or everything left is held aside. The loop
	// STOPS. Safe because the captured table cannot grow while Atlas is on:
	// captureOnCut is wired only when it is off (daemon.go), and the toggle is
	// startup-only.
	sweepDone sweepOutcome = iota
	// sweepProgress: something was delivered and something remains. Sweep
	// again at the base interval — the backoff is for a stuck Atlas, and Atlas
	// has just demonstrably taken a block.
	sweepProgress
	// sweepBlocked: nothing was delivered and something remains. Back off.
	sweepBlocked
)

// startRepublisher drains the captured-payload table, at daemon start and then
// on a widening interval until there is nothing left it would ever send.
//
// Only when Atlas is enabled — a machine that has always run with Atlas on
// finds an empty table and stops after one query. It never re-cuts and never
// asks the sidecar for anything: every row it sends is a payload captureOnCut
// already wrote down.
func startRepublisher(ctx context.Context, store *ledger.Store, cl atlas.Client) {
	if store == nil || cl == nil || !cl.Enabled() {
		return
	}
	go republishLoop(ctx, store, cl)
}

// republishLoop sweeps, waits, sweeps again — and the wait is decided AFTER
// the sweep it follows, so the first wait after the first blocked sweep is the
// base interval rather than twice it.
func republishLoop(ctx context.Context, store *ledger.Store, cl atlas.Client) {
	wait := republishInterval
	for {
		outcome := republishSweep(ctx, store, cl)
		if outcome == sweepDone {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if outcome == sweepProgress {
			wait = republishInterval
			continue
		}
		if wait < republishMaxInterval {
			wait *= 2
			if wait > republishMaxInterval {
				wait = republishMaxInterval
			}
		}
	}
}

// republishSweep drains ledger.Store's unsent-payload table in batches, oldest
// first, publishing each batch through the SAME atlas.Client every other path
// uses.
//
// ⚠️ **A FAILURE IS THREE DIFFERENT FACTS AND THIS FUNCTION ANSWERS EACH
// DIFFERENTLY** — the shape the daemon's other drains already use (see
// teleproxy's spool drain and clientevents/transport.go), because collapsing
// them is how either a bad row blocks every good one behind it or a hopeless
// one is retried forever:
//
//	REJECTED   (401/403) — the credential, which is machine-wide. Every
//	                       remaining batch would be told exactly the same
//	                       thing, so the sweep STOPS here. It is retried on a
//	                       later sweep because a credential is a thing that
//	                       gets fixed (the daemon re-onboards on a persistent
//	                       401 by itself; a person can re-pair in Settings).
//	UNAVAILABLE (net/5xx/captive portal) — nothing is wrong with the payloads
//	                       and nothing about them will change. END the sweep,
//	                       back off, try the same batch again later.
//	REFUSED    (other 4xx) — Atlas will not take THIS ENVELOPE. That is not a
//	                       verdict on any one row (publish.SendBlocks is
//	                       all-or-nothing and says so), so the batch is
//	                       re-posted one row at a time to find out which rows
//	                       Atlas actually objects to — see republishIsolate.
func republishSweep(ctx context.Context, store *ledger.Store, cl atlas.Client) sweepOutcome {
	delivered := 0
	outcome := func(blocked bool) sweepOutcome {
		if blocked && delivered > 0 {
			return sweepProgress
		}
		if blocked {
			return sweepBlocked
		}
		return sweepDone
	}
	// ⚠️ **A PAYLOAD IS REFUSED AT MOST ONCE PER SWEEP, and without this set it
	// would be refused five times in one.** A refused payload is not deleted,
	// so the very next batch this sweep asks for still leads with it: the loop
	// would re-offer, re-refuse and exhaust ledger.UnsentRefusalLimit inside a
	// single pass, in seconds. That would leave the bound counting REQUESTS
	// when the whole point of it — see republishInterval's backoff — is to
	// count OCCASIONS, spread over hours, so that "genuinely refused" means
	// something a transient server-side 422 cannot fake.
	refusedThisSweep := map[ledger.BlockKey]bool{}
	for {
		select {
		case <-ctx.Done():
			return outcome(true)
		default:
		}

		offered, err := store.UnsentPayloads(republishBatchSize + len(refusedThisSweep))
		if err != nil {
			// Not "giving up": the loop will ask again. A ledger that cannot
			// be read is a fact about this machine's disk, not about Atlas,
			// and it is already logged once by the store itself.
			log.Printf("keld-agent: republish: could not read the captured blocks (%v); retrying on the next sweep", err)
			return outcome(true)
		}
		if len(offered) == 0 {
			// Nothing the drain will offer at all. That is either genuinely
			// empty or everything left is held aside, and those read very
			// differently to whoever is looking at the log.
			republishLogDrained(store, delivered)
			return outcome(false)
		}
		batch := make([]ledger.UnsentPayload, 0, republishBatchSize)
		for _, p := range offered {
			if refusedThisSweep[p.Key] || len(batch) == republishBatchSize {
				continue
			}
			batch = append(batch, p)
		}
		if len(batch) == 0 {
			// Everything still captured was already refused in this sweep.
			// Not drained, and not an error — just nothing more to learn now.
			return outcome(true)
		}

		rows, keys := republishDecode(store, batch)
		if len(rows) == 0 {
			continue // everything in this batch was corrupt; loop for the next one
		}

		status, sendErr := cl.SendBlocks(ctx, rows)
		now := time.Now().UTC()
		if sendErr == nil {
			for _, k := range keys {
				store.Sent(k, now)
				store.Received(k, status, now)
				store.DeleteUnsentPayload(k)
			}
			delivered += len(keys)
			continue
		}

		// Reuses the SAME classifier recordPublishFailed does, so a
		// republished block's failure reads identically to a live one's: an
		// HTTP answer (even a rejection) means the POST itself succeeded, so
		// only `received` fails and `sent` still gets marked — a raw transport
		// failure marks `sent` instead.
		stage, reason, httpStatus := classifyPublishFailure(sendErr)
		republishRecordFailure(store, keys, stage, reason, httpStatus, now)

		switch reason {
		case ledger.ReasonAtlasRejected:
			log.Printf("keld-agent: republish: Atlas rejected the credential (%v) — %s. "+
				"The sweep stops here because every remaining batch would get the same answer; "+
				"it retries on a later sweep, and re-pairing in Settings is the fix if it persists.",
				sendErr, republishRemaining(store))
			return outcome(true)
		case ledger.ReasonAtlasRefused:
			n, halted := republishIsolate(ctx, store, cl, rows, keys, sendErr, refusedThisSweep)
			delivered += n
			if halted {
				return outcome(true)
			}
			continue
		default:
			log.Printf("keld-agent: republish: could not reach Atlas (%v) — %s. "+
				"Nothing is wrong with the captured blocks; the same batch is retried on a later sweep.",
				sendErr, republishRemaining(store))
			return outcome(true)
		}
	}
}

// republishIsolate re-posts a refused batch ONE ROW AT A TIME.
//
// ⚠️ **WITHOUT THIS, ONE UNACCEPTABLE BLOCK HOLDS SEVEN GOOD ONES.** The batch
// verdict is unattributable by construction — POST /v1/signal/blocks answers
// about the envelope, and publish.SendBlocks' own comment states in capitals
// that an error "says nothing about which rows Atlas stored" — so the only way
// to learn which row Atlas objects to is to ask about each row by itself. The
// cost is paid only on the refusal path, which is rare, and it is at most
// republishBatchSize extra POSTs; the alternative measured badly in the real
// incident this file's header describes, where 41 captured blocks were held
// behind a single batch verdict.
//
// A row that lands alone is delivered and forgotten. A row REFUSED alone is a
// verdict about that row, so it is counted (ledger.RefuseUnsentPayload) and
// held aside once it reaches the limit. Anything else coming back mid-isolation
// — a credential rejection, an unreachable Atlas — is not a verdict about any
// row, so it halts the pass without counting anything against the rows it has
// not reached.
func republishIsolate(ctx context.Context, store *ledger.Store, cl atlas.Client,
	rows []publish.BlockEnrichment, keys []ledger.BlockKey, batchErr error,
	refusedThisSweep map[ledger.BlockKey]bool) (delivered int, halted bool) {
	log.Printf("keld-agent: republish: Atlas refused a batch of %d (%v) — re-posting each block on its own, "+
		"because a refused batch names the envelope and not the block inside it",
		len(rows), batchErr)

	for i, row := range rows {
		select {
		case <-ctx.Done():
			return delivered, true
		default:
		}
		k := keys[i]
		status, err := cl.SendBlocks(ctx, []publish.BlockEnrichment{row})
		now := time.Now().UTC()
		if err == nil {
			store.Sent(k, now)
			store.Received(k, status, now)
			store.DeleteUnsentPayload(k)
			delivered++
			continue
		}
		stage, reason, httpStatus := classifyPublishFailure(err)
		republishRecordFailure(store, []ledger.BlockKey{k}, stage, reason, httpStatus, now)
		if reason != ledger.ReasonAtlasRefused {
			log.Printf("keld-agent: republish: stopping the per-block pass (%v) — that is not a verdict on any "+
				"one block, so the rest of the batch keeps its place and is retried on a later sweep", err)
			return delivered, true
		}
		refusedThisSweep[k] = true
		n, heldAside := store.RefuseUnsentPayload(k)
		if heldAside {
			log.Printf("keld-agent: republish: Atlas has refused block %s@%d on its own %d times (%v) — "+
				"it is now held aside and will NOT be offered again. It stays in the ledger marked refused; "+
				"nothing behind it is blocked by it.", k.Session, k.Start, n, err)
			continue
		}
		log.Printf("keld-agent: republish: Atlas refused block %s@%d on its own (%v) — refusal %d of %d, "+
			"retried on a later sweep", k.Session, k.Start, err, n, ledger.UnsentRefusalLimit)
	}
	return delivered, false
}

// republishDecode turns captured payloads back into wire rows, dropping any
// that cannot be read.
func republishDecode(store *ledger.Store, batch []ledger.UnsentPayload) ([]publish.BlockEnrichment, []ledger.BlockKey) {
	rows := make([]publish.BlockEnrichment, 0, len(batch))
	keys := make([]ledger.BlockKey, 0, len(batch))
	for _, p := range batch {
		var row publish.BlockEnrichment
		if err := json.Unmarshal(p.Payload, &row); err != nil {
			// A payload this file itself wrote should always decode; if it
			// doesn't (a future schema change, disk corruption), holding it
			// forever would wedge the sweep on one bad row. Drop it rather
			// than retry something that can never succeed.
			log.Printf("keld-agent: republish: dropping unreadable captured payload for %s@%d: %v",
				p.Key.Session, p.Key.Start, err)
			store.DeleteUnsentPayload(p.Key)
			continue
		}
		rows = append(rows, row)
		keys = append(keys, p.Key)
	}
	return rows, keys
}

func republishRecordFailure(store *ledger.Store, keys []ledger.BlockKey,
	stage ledger.Stage, reason ledger.Reason, httpStatus int, now time.Time) {
	for _, k := range keys {
		if stage == ledger.StageReceived {
			store.Sent(k, now)
		}
		store.Failed(k, stage, reason, httpStatus, now)
	}
}

// republishRemaining phrases the real backlog for a log line.
//
// ⚠️ The line this replaces said "(%d block(s) remain captured)" with the size
// of the batch that had just failed — so a machine holding 41 captured blocks
// reported 8, every time.
func republishRemaining(store *ledger.Store) string {
	total, heldAside, err := store.UnsentCounts()
	if err != nil {
		return "the number of blocks still captured could not be read"
	}
	waiting := total - heldAside
	if heldAside > 0 {
		return fmt.Sprintf("%d block(s) still captured (%d of them held aside as refused)", waiting, heldAside)
	}
	return fmt.Sprintf("%d block(s) still captured", waiting)
}

// republishLogDrained reports a sweep that found nothing left to offer.
func republishLogDrained(store *ledger.Store, delivered int) {
	total, heldAside, err := store.UnsentCounts()
	switch {
	case err != nil:
		if delivered > 0 {
			log.Printf("keld-agent: republish: delivered %d captured block(s) to Atlas", delivered)
		}
	case heldAside > 0:
		log.Printf("keld-agent: republish: delivered %d captured block(s); %d of %d remaining are held aside "+
			"as refused by Atlas and will not be retried. Nothing further is queued, so the sweep stops.",
			delivered, heldAside, total)
	case delivered > 0:
		log.Printf("keld-agent: republish: delivered %d captured block(s) to Atlas; nothing left to send.", delivered)
	}
}
