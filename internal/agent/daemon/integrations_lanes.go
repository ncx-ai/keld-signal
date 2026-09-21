package daemon

import (
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/integrations"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

// integrationLanes is the daemon's single lane record, loaded from disk once
// and shared by the recorder and the route.
//
// An atomic pointer rather than a package var so the zero value is usable: the
// CLI links this package's siblings and a nil record is a no-op, never a
// panic — the same shape currentServiceHealth uses.
var integrationLanes atomic.Pointer[integrations.Lanes]

// setIntegrationLanes installs the record. Called once, at wire-up.
func setIntegrationLanes(l *integrations.Lanes) { integrationLanes.Store(l) }

// currentIntegrationLanes returns the record, loading one from disk on first
// use so a caller that never went through wire-up (a test, the route in
// isolation) still reads the real instants rather than an empty map.
func currentIntegrationLanes() *integrations.Lanes {
	if l := integrationLanes.Load(); l != nil {
		return l
	}
	l := integrations.LoadLanes()
	integrationLanes.CompareAndSwap(nil, l)
	return integrationLanes.Load()
}

// noteIntegrationLane records that one lane carried a pointer for one source.
//
// ⚠️ IT IS CALLED ON ARRIVAL, NOT ON PUBLISH. The question the pane asks is
// "did the hook fire", and a pointer that could not be resolved still answers
// yes. Recording on publish instead would make a tool whose transcripts moved
// read `broken · hook` — the capture lane blamed for a failure two stages
// downstream.
//
// ⚠️ KNOWN LIMIT: the call site is the enrichment worker, so with
// `ml_backend: "off"` no pointer is ever recorded. That is honest rather than
// convenient — with enrichment off nothing consumes a pointer — but it does
// mean the hook and watcher lanes read silent on such a machine, and `broken`
// there would need telemetry flowing to fire at all. It is the sibling of the
// per-source telemetry gap WS-C2 closes, and the same place to fix if it bites.
func noteIntegrationLane(j queue.Job) {
	origin := j.Origin
	switch origin {
	case integrations.OriginHook, integrations.OriginWatcher:
	default:
		// An unrecognised origin is dropped rather than recorded under a name
		// nothing reads: the two vocabularies must not drift silently.
		return
	}
	currentIntegrationLanes().RecordPointer(j.Source, origin, time.Now())
}

// bindPointerObserver makes the ingress record a lane for every pointer the
// daemon ACCEPTS, not only for those an enrichment worker later picks up.
//
// ⚠️ Without it, `ml_backend: "off"` reports a false `broken`. That mode runs no
// worker — /enrich accepts-and-discards — while telemetry is explicitly
// unaffected and keeps reporting. One expected lane active and another silent is
// the predicate for `broken`, so the pane would have blamed a hook that fired
// correctly every time, on every machine with enrichment off.
//
// The worker's own call site stays: a pointer drained from the on-disk spool
// after a daemon restart never passed through the ingress at all, and that hook
// fired too.
func bindPointerObserver() {
	ingress.OnPointer = func(p spool.Pointer) {
		noteIntegrationLane(queue.Job{Source: p.Source.ID, Origin: p.Source.Origin})
	}
}

// watchOffer is the transcript watcher's enqueue seam: record the lane, then
// offer the pointer to the queue.
//
// ⚠️ THE RECORD DOES NOT CONSULT THE QUEUE'S ANSWER, AND THAT IS THE WHOLE
// POINT. On a machine whose hook is wired the hook wins the race every time, so
// the watcher's offer comes back `Duplicate` and the job never reaches the
// worker — which was the only place a watcher lane fact was ever written. The
// watcher lane therefore read silent on exactly the machines where it was
// working correctly, and silence on an expected lane is one half of `broken`.
// The hook↔watcher overlap is DESIGNED (queue.Complete exists for it); it must
// no more read as a dead lane than it reads as overload.
//
// Recorded on arrival, before the offer, for the reason noteIntegrationLane
// gives: "did the watcher see this prompt" is answered yes by a pointer the
// daemon then chose not to enrich.
func watchOffer(q *queue.Queue) func(spool.Pointer) {
	return func(p spool.Pointer) {
		noteIntegrationLane(queue.Job{Source: p.Source.ID, Origin: p.Source.Origin})
		q.Offer(ingress.JobFrom(p))
	}
}

// watchPrompt is the watcher's EXTRACTION seam (watch.WithPromptObserver): one
// call per genuine user prompt the watcher read, whether or not a pointer was
// offered for it.
//
// ⚠️ IT EXISTS BECAUSE watchOffer ABOVE IS NOT ENOUGH, AND THE GAP WAS A FALSE
// `broken`. A transcript's FIRST SIGHTING under forward-only offers nothing —
// the cursor jumps to EOF so a fresh install does not enrich a machine's whole
// past — so `watchOffer` never runs for a session that is created, written and
// finished between two 5-second polls. That is the ordinary shape of
// `codex exec` and `claude -p`, and of any session whose first prompt lands
// inside the poll gap. Measured on this machine: a live Codex session
// capturing normally — its usage mirrored, its blocks cut and delivered —
// while the pane reported `broken · watcher`, because the one fact the pane
// reads was written only on a path that branch never takes.
//
// Both seams stay. They are two call sites of one definition, not two
// definitions: the offer covers a pointer the queue then deduped, this covers a
// prompt no pointer was ever built for, and the instant is idempotent so an
// overlap costs nothing. The origin is fixed rather than read off a pointer
// because there is no pointer here — an extraction by the watcher is a watcher
// fact by construction.
func watchPrompt() func(source, transcriptPath string) {
	return func(source, _ string) {
		noteIntegrationLane(queue.Job{Source: source, Origin: integrations.OriginWatcher})
	}
}
