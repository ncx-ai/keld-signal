package daemon

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
)

// warmPollInterval is how often a gate re-checks the state it tracks.
// Frequent enough to notice a transition quickly, cheap enough for the
// endpoints it polls (the sidecar's /metrics and /health).
const warmPollInterval = 500 * time.Millisecond

// warmGate holds the latest observed readiness state as a non-latching atomic
// bool, refreshed by a background poller.
//
// It exists for two reasons, and both matter. First, correctness:
// Supervisor.Ready() latches true after the first /health success and never
// reflects a later idle-kill reload, so the Worker needs live state to avoid
// counting model-load time against a job's deadline. Second, COST: the Worker
// calls its gate at the top of every job and waitWarm re-calls it roughly
// every 20ms while a job waits, so the gate must be a memory read. Probing the
// service inside the gate instead means thousands of loopback requests per
// deferred job — and, against a service that accepts TCP but never answers,
// the client's full timeout on every single call.
//
// Two gates are built on it: model-resident warmth for ml_backend "auto"
// (Client.WorkerReady, the sidecar's /metrics worker.state) and service health
// for "deterministic" (Client.Healthy, /health). The type keeps its "warm"
// name from the first use; both are the same thing mechanically — a polled,
// cached, non-latching readiness bit.
type warmGate struct{ warm atomic.Bool }

func newWarmGate() *warmGate { return &warmGate{} }

// run polls ready on interval, storing each result, until ctx is cancelled.
// Intended to run in its own goroutine.
func (g *warmGate) run(ctx context.Context, ready func(context.Context) bool, interval time.Duration) {
	g.warm.Store(ready(ctx)) // seed immediately so we don't wait a full interval
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			g.warm.Store(ready(ctx))
		}
	}
}

// Warm reports the most recently observed model-resident state (cheap).
func (g *warmGate) Warm() bool { return g.warm.Load() }

// serviceHealthGate returns deterministic mode's readiness gate: a cached
// "the analysis service is answering /health right now" bit, refreshed in the
// background until ctx is cancelled.
//
// It is the same mechanism "auto" uses for model warmth, deliberately — only
// the probe differs (/health rather than /metrics worker.state, because this
// mode never loads the model, so warmth never arrives). Handing the raw
// Client.Healthy closure to the Worker as a gate would be a live HTTP call per
// call site; see warmGate's doc for what that costs.
func serviceHealthGate(ctx context.Context, c *sidecar.Client) func() bool {
	g := newWarmGate()
	go g.run(ctx, c.Healthy, warmPollInterval)
	return g.Warm
}
