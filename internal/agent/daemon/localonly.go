package daemon

import (
	"context"
	"log"
	"sync/atomic"

	"github.com/ncx-ai/keld-signal/internal/agent/publish"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// ⚠️ **LOCAL-ONLY MODE IS ENFORCED HERE, AND THE FIRST ATTEMPT DID NOT WORK.**
//
// `atlas.Off` made the NEW connector incapable of reaching the network, and the
// comment on it said a boolean checked at each call site "would have been one
// forgotten branch away from a machine that promised local-only and phoned home
// anyway". That is exactly what shipped for one commit: the enrichment
// publisher, the tick, the settings poll, the client-event reporter and the
// telemetry proxy all predate the boundary and each held its own endpoint. An
// end-to-end run with `KELD_ATLAS=0` dialled Atlas once every two seconds and
// logged a connection-refused line for every prompt in the corpus.
//
// The lesson is that a boundary only binds what is routed through it. So this
// file does not add another check beside the others: it replaces the SENDER
// every remaining outbound path already takes, with one that has no transport
// and no endpoint. A path that forgets to consult a flag still cannot send,
// because the thing it was handed cannot.
//
// What local-only means, precisely:
//   - nothing is POSTed to Atlas: no enrichments, no blocks, no window rows, no
//     feature rows, no client events, no forwarded tool telemetry
//   - nothing is FETCHED from Atlas: no settings poll, so no org vocabulary, no
//     remote toggles and no auto-update
//   - everything else runs unchanged: the watcher, the sidecar, the block
//     cutter, the ledger, deterministic attribution and the page
//
// It is not a privacy claim about the machine — the sidecar still reads
// transcripts, which is the whole product — it is a claim about the WIRE.
type localOnlySender struct {
	dropped atomic.Int64
	warned  atomic.Bool
}

// Send discards an enrichment and counts it.
//
// ⚠️ **It returns nil, deliberately.** An error here would make the worker
// treat the job as failed, re-spool it, and eventually quarantine it — turning
// "you asked us not to send this" into a growing spool of poison rows and a
// stream of failure events on a machine where nothing is wrong. Discarding is
// the honest action: the ledger still records that the block was cut and
// measured, and its `sent` cell reads n/a with reason atlas_off, which is what
// the page shows instead of a column of crosses.
func (s *localOnlySender) Send(publish.Enrichment) error {
	n := s.dropped.Add(1)
	if s.warned.CompareAndSwap(false, true) {
		log.Printf("keld-agent: Send to Atlas is off — enrichments are computed and kept locally, not published")
	}
	_ = n
	return nil
}

func (s *localOnlySender) SendBlocks([]publish.BlockEnrichment) error { return nil }

// SendWindow is the tick's route. Same discard, same reason: a tick window that
// errored would re-enter the tick's retry path and stop its cursor advancing,
// so a machine deliberately offline would look like one that is stuck.
func (s *localOnlySender) SendWindow(publish.WindowEnrichment) error { return nil }

func (s *localOnlySender) Dropped() int64 { return s.dropped.Load() }

// pollSettingsIfOnline runs the org settings poll only when Atlas is on.
//
// ⚠️ The poll is not merely one more request: it carries the auto-update pin,
// the org's feature toggles and its project vocabulary. A local-only machine
// that polled would be reachable from the server for a binary swap, which is
// the opposite of what the toggle promises.
func pollSettingsIfOnline(ctx context.Context, on bool, run func(context.Context)) {
	if !on {
		log.Printf("keld-agent: Send to Atlas is off — not polling org settings; " +
			"org projects, remote toggles and auto-update are all inactive on this machine")
		return
	}
	go run(ctx)
}

// senderFor returns what the enrichment worker and the tick publish through:
// the real publisher, or one that cannot reach anything.
func senderFor(set settings.Settings, real Sender) Sender {
	if set.AtlasEnabled() {
		return real
	}
	return &localOnlySender{}
}
