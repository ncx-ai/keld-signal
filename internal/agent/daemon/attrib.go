package daemon

import (
	"context"
	"log"
	"path/filepath"

	"github.com/ncx-ai/keld-signal/internal/agent/attrib"
	"github.com/ncx-ai/keld-signal/internal/agent/blocks"
	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// THE PROJECT ATTRIBUTION PATH's daemon wiring — the GO INTEGRATION half that
// drives the sidecar's /attribute for every block the emitter just published,
// and re-publishes the block once attribution has a terminal answer. The
// durable job store + retry/pending logic live in internal/agent/attrib; this
// file is only the seams it needs (the publisher, the digester it re-fetches
// through, the transcript facts, and the switch that ships it OFF) — mirroring
// blocks.go one path over.

// attribStoreDir is where attribution jobs live: beside the enrich spool's own
// subdirectories, not inside spool.db (a different durable-queue shape — see
// attrib.Store's own doc comment on why it is a flat JSON-file store rather
// than the SQL-backed spool package).
func attribStoreDir() string { return filepath.Join(paths.SpoolDir(), "attrib") }

// startAttributor wires and starts the attribution loop when the
// `attribution` gate is on, and returns the hook the block emitter should
// call after every successful publish (blocks.Emitter.OnPublished). nil when
// the gate is off, any required capability is missing, or the block digester
// itself is nil — attribution has nothing to schedule against without it.
func startAttributor(ctx context.Context, dig blocks.Digester, cl attrib.AttributeClient,
	ingestEndpoint string, token func() string, actor string,
	emitter *clientevents.Emitter, attribConfigured bool) func(rows []publish.BlockEnrichment, path string) {
	if !attrib.Enabled(attribConfigured) || dig == nil || cl == nil || token == nil {
		return nil
	}
	pub := publish.New(signalBlocksEndpoint(ingestEndpoint), token, actor)
	// Its own facts cache, mirroring startBlockEmitter's: a re-fetch through
	// the Digester needs the same repository facts (repo/branch) the original
	// publish did, and the two caches are cheap, bounded, per-transcript maps.
	facts := newFactsCache()
	st := attrib.NewStore(attribStoreDir())
	a := attrib.New(st, cl, pub,
		func(path string) enrich.ResolvedFacts { return facts.forTranscript(path).resolved() },
		actor, dig)
	interval := attrib.IntervalFromEnv()
	log.Printf("keld-agent: project attribution ON (sweeping every %s)", interval)
	if emitter != nil {
		emitter.EmitExempt("attribution.enabled", clientevents.SevInfo, map[string]any{
			"interval_s": int(interval.Seconds()),
		})
	}
	go a.Run(ctx, interval)
	// blocks.Emitter.OnPublished hands over a whole CHUNK (batchSize rows) at
	// once; attrib.Attributor.Schedule takes one block. Fan the chunk out —
	// each Schedule call is a cheap file write (see its own doc comment on
	// why it never blocks), so a chunk of up to batchSize is not a concern.
	return func(rows []publish.BlockEnrichment, path string) {
		for _, row := range rows {
			a.Schedule(row, path)
		}
	}
}
