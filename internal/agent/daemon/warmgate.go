package daemon

import (
	"context"
	"sync/atomic"
	"time"
)

// warmPollInterval is how often the warm gate re-checks model-resident state.
// Frequent enough to notice a warm transition quickly, cheap enough for the
// sidecar's /metrics endpoint.
const warmPollInterval = 500 * time.Millisecond

// warmGate holds the latest observed "model resident now" state as a
// non-latching atomic bool. It exists because Supervisor.Ready() latches true
// after the first /health success and never reflects a later idle-kill reload;
// the Worker needs the live state to avoid counting model-load time against a
// job's deadline.
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
