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

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/atlas"
)

const (
	// republishBatchSize mirrors the block emitter's own batchSize
	// (internal/agent/blocks/emitter.go): the granularity at which progress
	// is kept if the network drops mid-drain, not a throughput knob.
	republishBatchSize = 8
	// republishMaxAttempts bounds the sweep so a machine holding a month of
	// local-only blocks does not hammer Atlas forever on first connect if
	// something is actually wrong (a bad credential, a firewalled host).
	republishMaxAttempts = 5
)

// republishBaseBackoff is a var, not a const, so a test exercising the
// give-up path does not have to spend the real exponential backoff
// (2s+4s+8s+16s = 30s) to prove it happened.
var republishBaseBackoff = 2 * time.Second

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

// startRepublisher runs the drain sweep exactly ONCE per daemon start, only
// when Atlas is enabled — a machine that has always run with Atlas on finds
// an empty table and returns immediately. It never re-cuts and never asks the
// sidecar for anything: every row it sends is a payload captureOnCut already
// wrote down.
func startRepublisher(ctx context.Context, store *ledger.Store, cl atlas.Client) {
	if store == nil || cl == nil || !cl.Enabled() {
		return
	}
	go republishSweep(ctx, store, cl)
}

// republishSweep drains ledger.Store's unsent-payload table in batches,
// oldest first, publishing each batch through the SAME atlas.Client every
// other path uses. A batch that fails is retried with exponential backoff up
// to republishMaxAttempts before the sweep gives up for this run — the next
// daemon start (or, in the future, a live Atlas-toggle event) tries again
// from wherever the table stands, since nothing here assumes it runs to
// completion.
func republishSweep(ctx context.Context, store *ledger.Store, cl atlas.Client) {
	attempts := 0
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		batch, err := store.UnsentPayloads(republishBatchSize)
		if err != nil {
			log.Printf("keld-agent: republish: could not read captured blocks, giving up for this run: %v", err)
			return
		}
		if len(batch) == 0 {
			return // caught up
		}

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
		if len(rows) == 0 {
			continue // everything in this batch was corrupt; loop for the next one
		}

		status, sendErr := cl.SendBlocks(ctx, rows)
		now := time.Now().UTC()
		if sendErr != nil {
			// Reuses the SAME classifier recordPublishFailed does, so a
			// republished block's failure reads identically to a live one's:
			// an HTTP answer (even a rejection) means the POST itself
			// succeeded, so only `received` fails and `sent` still gets
			// marked — a raw transport failure marks `sent` instead.
			stage, reason, httpStatus := classifyPublishFailure(sendErr)
			for _, k := range keys {
				if stage == ledger.StageReceived {
					store.Sent(k, now)
				}
				store.Failed(k, stage, reason, httpStatus, now)
			}
			attempts++
			if attempts >= republishMaxAttempts {
				log.Printf("keld-agent: republish: giving up after %d attempts (%d block(s) remain captured): %v",
					attempts, len(batch), sendErr)
				return
			}
			wait := republishBaseBackoff * time.Duration(1<<uint(attempts-1))
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}

		attempts = 0
		for _, k := range keys {
			store.Sent(k, now)
			store.Received(k, status, now)
			store.DeleteUnsentPayload(k)
		}
	}
}
