package daemon

import (
	"sync"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/promptlog"
)

// mirrorDropReporter turns the usage mirror's per-record drop hook into ONE
// client event per reason per daemon run, carrying the running count for that
// reason at the moment it fired.
//
// Per reason and not per record because the loss comes in runs: a machine that
// is unpaired drops every record until it pairs, and three hundred identical
// events say less than one. The first drop per reason is emitted at once, so
// the fleet learns about a silently-losing machine within one prompt of the
// loss starting rather than at some later summary. `not_paired` is info — it is
// the expected state between install and login — and the other three are warn,
// because usage is going missing on a machine that thought it was sending.
func mirrorDropReporter(emitter *clientevents.Emitter, counts func() map[string]int64) func(reason string) {
	var mu sync.Mutex
	said := map[string]bool{}
	return func(reason string) {
		if emitter == nil {
			return
		}
		mu.Lock()
		first := !said[reason]
		said[reason] = true
		mu.Unlock()
		if !first {
			return
		}
		sev := clientevents.SevWarn
		if reason == promptlog.DropNotPaired {
			sev = clientevents.SevInfo
		}
		emitter.Emit("telemetry.mirror_dropped", sev, map[string]any{
			"reason": reason, "count": counts()[reason]})
	}
}
