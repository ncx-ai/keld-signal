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

// demandModelsForAttribution wraps the block emitter's OnPublished hook so a
// scheduled attribution job also signals "something may want the models" — the
// same demand-signal shape the signal-embeddings path uses its own advance hook
// for. Returns next unchanged when there is nothing to wrap (attribution off),
// so a machine that never turned it on pays nothing.
//
// ⚠️ GATED ON A KNOWN NON-EMPTY PROJECT LIST (I7). The two models are ~1.2 GB
// (encoder) + ~3 GB (verifier). With nothing declared to match against, every
// /attribute answers skipped:no_projects WITHOUT opening a transcript or
// loading a model, so the 4.2 GB buys literally nothing — and since Atlas does
// not serve `projects` yet, that is today every machine without
// KELD_PROJECTS_FILE. An org switching attribution on early would otherwise
// pull both models onto its whole fleet for an answer that needs neither.
//
// `known` is read LIVE, per published block, never captured: a project list
// arriving on a later settings poll starts the fetch with no restart — the
// same live-re-check the encoder provisioner's own `gate` does for the org's
// `features` toggle. Every demand func must be nil-receiver-safe (both are).
func demandModelsForAttribution(next func(rows []publish.BlockEnrichment, path string),
	known func() bool, demand ...func()) func(rows []publish.BlockEnrichment, path string) {
	if next == nil {
		return nil
	}
	return func(rows []publish.BlockEnrichment, path string) {
		if known == nil || known() {
			for _, d := range demand {
				d()
			}
		}
		next(rows, path)
	}
}

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
// projects is the daemon's belief about the declared project list and the way
// to re-assert it to the sidecar — nil when attribution has no /projects
// channel this run. See attrib.Attributor.WithProjects: it is what makes a
// `skipped:no_projects` answer non-terminal.
func startAttributor(ctx context.Context, dig blocks.Digester, cl attrib.AttributeClient,
	ingestEndpoint string, token func() string, actor string,
	emitter *clientevents.Emitter, attribConfigured bool,
	projectsKnown func() bool, repostProjects func()) func(rows []publish.BlockEnrichment, path string) {
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
		actor, dig).
		WithProjects(projectsKnown, repostProjects).
		WithEmitter(emitter)
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
