package daemon

import (
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/integrations"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
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
