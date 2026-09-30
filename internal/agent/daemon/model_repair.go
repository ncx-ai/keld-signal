package daemon

import (
	"context"
	"log"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

// Naming the models of blocks recorded before 2026-09-29.
//
// Until that day a block under the evidence floor was stored with no model even
// when every one of its requests named one (see dominantModel), and so with no
// price. The fix names every NEW block; this names the old ones, once, by
// asking the analysis service again — it still holds those sessions, so the
// model is read, not guessed. A block the service saw no model in stays
// unnamed.

// blockModelLookup is the running block emitter's ModelsFor, or nil when the
// emitter is off. Set by startBlockEmitter, for the same reason blockSweepNow
// is: the emitter is optional, and threading a usually-nil hook through the
// wiring would cost more than it buys.
var blockModelLookup atomic.Pointer[func(session string, starts []int64, now time.Time) (map[int64]string, bool)]

func setBlockModelLookup(fn func(session string, starts []int64, now time.Time) (map[int64]string, bool)) {
	if fn == nil {
		blockModelLookup.Store(nil)
		return
	}
	blockModelLookup.Store(&fn)
}

// unnamedModels is the ledger's side of the repair.
type unnamedModels interface {
	UnnamedModelBlocks() []ledger.UnnamedBlock
	NameModel(k ledger.BlockKey, model string, usd float64)
}

type modelLookup func(session string, starts []int64, now time.Time) (map[int64]string, bool)

// repairUnnamedModels names what it can, retrying every `every` while the
// analysis service has not answered for some session (it may still be
// starting), and stops for good once every session has been answered — or
// after `attempts` tries, so a service that never comes up costs a bounded
// number of wake-ups rather than a loop for the daemon's life.
func repairUnnamedModels(ctx context.Context, store unnamedModels, lookup func() modelLookup,
	price func(model string, in, out, cacheRead, cacheCreation int64) (float64, bool),
	every time.Duration, attempts int) int {
	named := 0
	for try := 0; try < attempts; try++ {
		fn := lookup()
		if fn == nil {
			return named
		}
		bySession := map[string][]ledger.UnnamedBlock{}
		for _, u := range store.UnnamedModelBlocks() {
			bySession[u.Key.Session] = append(bySession[u.Key.Session], u)
		}
		pending := false
		for session, us := range bySession {
			if ctx.Err() != nil {
				return named
			}
			starts := make([]int64, len(us))
			for i, u := range us {
				starts[i] = u.Key.Start
			}
			models, ok := fn(session, starts, time.Now())
			if !ok {
				pending = true
				continue
			}
			for _, u := range us {
				m := models[u.Key.Start]
				if m == "" {
					continue
				}
				usd, _ := price(m, u.Input, u.Output, u.CacheRead, u.CacheCreation)
				store.NameModel(u.Key, m, usd)
				named++
			}
		}
		if !pending {
			break
		}
		select {
		case <-ctx.Done():
			return named
		case <-time.After(every):
		}
	}
	if named > 0 {
		log.Printf("keld-agent: named the model of %d earlier block(s) recorded without one", named)
	}
	return named
}
