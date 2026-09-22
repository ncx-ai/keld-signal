package daemon

import (
	"sync/atomic"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/integrations"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// integrationSink holds the live emitter adapter.
//
// The report route is built in v3.routes(), which runs BEFORE the emitter
// exists — the emitter is constructed from the config awaitConfig waits for. So
// the route takes a sink that is resolved per request rather than captured at
// mount time; before onboarding it queues nothing and says so, and the bundle
// is still written to disk, which is the half a person can send us by hand.
var integrationSink atomic.Pointer[emitterSink]

func setIntegrationSink(e *clientevents.Emitter) {
	if e == nil {
		return
	}
	integrationSink.Store(&emitterSink{e: e})
}

// currentIntegrationSink returns a sink that resolves late. It is never nil, so
// the route never has to branch on it.
func currentIntegrationSink() integrations.Sink { return lateSink{} }

type lateSink struct{}

func (lateSink) Emit(code, severity string, fields map[string]any) {
	if s := integrationSink.Load(); s != nil {
		s.Emit(code, severity, fields)
	}
}

func (lateSink) EmitExempt(code, severity string, fields map[string]any) {
	if s := integrationSink.Load(); s != nil {
		s.EmitExempt(code, severity, fields)
	}
}

// describeIntegrationForReport answers one row's state for the bundle, from the
// SAME snapshot the route and doctor read. A second derivation here would be a
// second state rule, which AC-8 exists to prevent.
func describeIntegrationForReport(id string) (state, toolVersion string, findings []string, ok bool) {
	// ⚠️ The Developer switch is resolved here too, not defaulted. A snapshot
	// taken with it off on a machine that has it on would report the otel lane
	// as not expected, so the bundle would disagree with the pane about the same
	// row — and the bundle is what someone reads when the pane confused them.
	opts := integrations.Options{ToolOTLP: settings.Load().ToolOTLPEnabled()}
	for _, in := range integrations.Snapshot(integrations.Deps{}, opts).Integrations {
		if in.ID != id {
			continue
		}
		var f []string
		for _, s := range in.Surfaces {
			if s.WaitingOn != "" {
				f = append(f, string(s.Kind)+":waiting_on_"+string(s.WaitingOn))
			}
		}
		if in.BrokenLane != "" {
			f = append(f, "broken_lane:"+string(in.BrokenLane))
		}
		return string(in.State), in.ToolVersion, f, true
	}
	return "", "", nil, false
}
