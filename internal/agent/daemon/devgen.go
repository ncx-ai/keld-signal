package daemon

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

// ⚠️ **THE GENERATE BUTTON DRIVES THE PIPELINE; IT DOES NOT HOPE FOR IT.**
//
// The route used to write a transcript and answer immediately, and everything
// after that ran on timers: the watcher's poll, then the block emitter's sweep,
// which is FIVE MINUTES by default. So the button reported success and the page
// showed nothing for minutes. That is not a latency detail to explain away — a
// control that says it did something, and then visibly did not, is broken. The
// user story is "press it, the block is there", and the timers are an
// implementation detail the button has no business exposing.
//
// So the daemon hands the route a hook that does the work synchronously, in the
// order the pipeline would have done it eventually:
//
//  1. ingest the transcript into the reference series (the sidecar's /ingest,
//     the same call the watcher's advance signal makes — not a private path);
//  2. add the transcript to the block emitter's active set (Advance, the same
//     seam the watcher feeds);
//  3. run ONE sweep (Sweep, the same function the ticker calls);
//  4. wait until the block is actually recorded in the ledger, because that is
//     what the page reads and therefore the only thing that makes the button's
//     answer true.
//
// Nothing here is a second implementation of anything. Every step is the
// function the timer path already calls; the only difference is that they are
// called NOW rather than within five minutes.
//
// ⚠️ **STEP 4 CAN FAIL, AND THEN THE ROUTE SAYS SO.** A generated session whose
// block is not closed (the clock moved, the store is behind, the sidecar is
// down) must not answer 200 with a cheerful tick. `blocks: 0` is reported and
// the page says the block did not land, which is the honest version of what the
// old code said implicitly by staying silent.

// blockSweepNow is how a caller asks the block emitter for one immediate pass.
// A package-level atomic for the reason `blockAdvance` beside it is one: the
// emitter is optional and off by default, so widening the wiring to thread a
// usually-nil hook through would cost more than it buys.
var blockSweepNow atomic.Pointer[func(ctx context.Context, path string, now time.Time) int]

func setBlockSweep(fn func(ctx context.Context, path string, now time.Time) int) {
	if fn == nil {
		blockSweepNow.Store(nil)
		return
	}
	blockSweepNow.Store(&fn)
}

// devIngest is the sidecar's ingest signal plus the facts resolver that gives a
// transcript its repository identity.
//
// ⚠️ **RESOLVED LAZILY, BECAUSE THE ROUTES ARE BUILT BEFORE THE SIDECAR IS.**
// `Run` passes its route list INTO `wireEnrichment`, which is what produces the
// sidecar client — so at the moment `DevGenerateRoute` is constructed there is
// no client to hand it. A package-level atomic set afterwards is the idiom
// `blockAdvance` and `tickObserver` already use for exactly this ordering.
//
// The facts matter as much as the signal: without the repository identity the
// block is cut with NO `repo` dimension, and dims are recorded once at cut time
// and never revised — so the Projects half of the story could never pass, and
// it would look like an attribution bug rather than an ingest one.
type devIngest struct {
	signal func(path string, resolved enrich.ResolvedFacts) bool
	facts  func(path string) enrich.ResolvedFacts
}

var devIngestHook atomic.Pointer[devIngest]

// devDriveMu serialises the pipeline drive. See devGenerateHook.
var devDriveMu sync.Mutex

func setDevIngest(signal func(string, enrich.ResolvedFacts) bool,
	facts func(string) enrich.ResolvedFacts) {
	if signal == nil && facts == nil {
		devIngestHook.Store(nil)
		return
	}
	devIngestHook.Store(&devIngest{signal: signal, facts: facts})
}

// devGenerateHook returns what ingress calls after it has written a generated
// transcript. It reports how many blocks that transcript now has in the ledger.
func devGenerateHook(ctx context.Context, led *ledger.Store) func(session, path string) int {
	if led == nil {
		return nil
	}
	return func(session, path string) int {
		// ⚠️ **SERIALISED, BECAUSE THE BUTTON DELIBERATELY IS NOT.** The page
		// leaves the control pressable so a developer can queue several blocks,
		// which means this hook can be entered concurrently — and `Emitter.Sweep`
		// walks and rewrites the emitter's own active-set state, which the timer
		// path only ever calls from a single goroutine. Two sweeps interleaving
		// there would corrupt exactly the cursor that decides which blocks have
		// already been emitted.
		//
		// A mutex rather than a queue with a bound: each press costs a few
		// seconds, presses come from a human hand, and making the second click
		// wait for the first is the honest behaviour — the alternative is
		// dropping a press the person watched themselves make.
		devDriveMu.Lock()
		defer devDriveMu.Unlock()

		var signalIngest func(string, enrich.ResolvedFacts) bool
		resolved := enrich.ResolvedFacts{}
		if h := devIngestHook.Load(); h != nil {
			signalIngest = h.signal
			if h.facts != nil {
				resolved = h.facts(path)
			}
		}
		if signalIngest != nil {
			// One attempt, like the watcher's. A refusal here is not fatal:
			// /analyze and /blocks both ingest on demand, so the sweep below
			// still has a chance.
			if !signalIngest(path, resolved) {
				log.Printf("keld-agent: dev generate: the sidecar refused an ingest signal for %s", path)
			}
		}
		if fn := blockAdvance.Load(); fn != nil {
			(*fn)("claude_code", path)
		}

		// Sweep until the ledger holds this session, bounded. The first sweep
		// usually suffices; a retry exists because ingest is asynchronous
		// inside the sidecar and the store can be a beat behind the signal.
		deadline := time.Now().Add(devGenerateWait)
		for {
			if fn := blockSweepNow.Load(); fn != nil {
				// THIS transcript only. Sweeping the whole active set is
				// O(transcripts) — 59 on a real machine — and the caller is a
				// person holding down a button. See Emitter.SweepPath.
				(*fn)(ctx, path, time.Now())
			}
			if n := countLedgerBlocks(led, session); n > 0 {
				return n
			}
			if time.Now().After(deadline) || ctx.Err() != nil {
				return 0
			}
			select {
			case <-ctx.Done():
				return 0
			case <-time.After(devGeneratePoll):
			}
		}
	}
}

const (
	// Bounded so the route cannot hang a page load. Measured on a real machine:
	// ingest plus one sweep lands the block in about two seconds; the budget is
	// generous because a cold sidecar has to open the store first.
	devGenerateWait = 45 * time.Second
	devGeneratePoll = 2 * time.Second
)

func countLedgerBlocks(led *ledger.Store, session string) int {
	recs, err := led.BlocksSince(time.Now().AddDate(0, 0, -2), 5000)
	if err != nil {
		return 0
	}
	n := 0
	for _, r := range recs {
		if r.Session == session {
			n++
		}
	}
	return n
}
