package daemon

import (
	"log"
	"sync/atomic"

	"github.com/ncx-ai/keld-signal/internal/spool"
)

// ⚠️ **COLLECT ALWAYS, PAIR TO SEND.**
//
// `Run` used to start NOTHING but the integrations detector until `awaitConfig`
// saw `~/.keld/hook.json`. So a machine between install and login collected
// nothing at all: no telemetry proxy listening (every AI tool `keld signal
// setup` had already configured was posting into a closed port), no transcript
// watcher, no block emitter, no enrichment. That gate is left over from a design
// that no longer exists — the hook used to POST usage telemetry straight to
// Atlas, and without a token there genuinely was nothing to do. Every lane now
// has a local store or a bounded spool, so collection needs Atlas for nothing.
// Only DELIVERY does.
//
// The split is therefore: everything that collects is constructed and started
// immediately; everything that sends resolves its endpoint through `pairing`,
// which answers "" until the pairing arrives. A sender handed "" must HOLD —
// spool the batch, keep the cursor, re-spool the pointer — never report success
// and never drop. `publish.ErrNotPaired`, `clientevents.ErrNotPaired` and
// `settings.ErrNotPaired` are the three ways that is said.

// pairing is the machine's Atlas pairing: the ingest endpoint written to
// ~/.keld/hook.json by `keld signal setup` or `POST /v1/config`.
//
// It holds the ENDPOINT only. The token beside it lives in creds.Token, which
// already existed for exactly this reason (a self-heal re-auth swaps it live),
// and this is the same idea applied to the address — which, before collectors
// started early, was the one thing that genuinely could not be known at
// construction time.
type pairing struct {
	endpoint atomic.Value // string
}

func newPairing() *pairing {
	p := &pairing{}
	p.endpoint.Store("")
	return p
}

// set records the pairing. Called once, when awaitConfig returns.
func (p *pairing) set(endpoint string) { p.endpoint.Store(endpoint) }

// ingest is the base endpoint every other route is derived from, or "" while
// this machine is unpaired.
func (p *pairing) ingest() string {
	v := p.endpoint.Load()
	if v == nil {
		return ""
	}
	return v.(string)
}

// paired reports whether an endpoint has arrived.
func (p *pairing) paired() bool { return p.ingest() != "" }

// deriveEndpoint composes a base-endpoint resolver with one of the route
// derivations (enrichEndpoint, signalBlocksEndpoint, ...), preserving ""
// through the composition.
//
// ⚠️ THE EMPTY CASE HAS TO SURVIVE. Every derivation appends a path, so
// deriving from "" would produce "/v1/signal/blocks" — a relative URL that
// fails at http.NewRequest and classifies as a PERMANENT error, which is how a
// held cursor becomes an advanced one and a spooled batch becomes a deleted one.
func deriveEndpoint(base func() string, route func(string) string) func() string {
	return func() string {
		b := base()
		if b == "" {
			return ""
		}
		return route(b)
	}
}

// sendersStarted records that this daemon has reached the point where it can
// DELIVER: the pairing has been read and everything that POSTs to Atlas on a
// clock of its own has been constructed and started.
//
// A package-level marker rather than a return value because nothing in the
// process needs to read it — the collectors do not care whether the machine is
// paired, which is the whole point — while the tests that pin this invariant
// need one unambiguous answer to "has anything that sends been started yet".
var sendersStarted atomic.Bool

// enrichHold is how the enrichment worker learns that this machine cannot
// publish yet. A package-level seam rather than a tenth parameter on Worker,
// mirroring blockAdvance and tickObserver exactly: it is optional, absent in
// every existing caller (eval harness, unit tests), and needs nothing from the
// worker but a yes or no.
//
// ⚠️ **ENRICHMENT IS THE ONE COLLECTOR WITH NOWHERE LOCAL TO PUT ITS OUTPUT.**
// A block is cut into the ledger and re-offered by a held cursor; a telemetry
// batch goes to the proxy's spool; an enrichment has neither — there is no
// on-disk store of finished profiles, so running the pipeline while unpaired
// would compute a profile and then discard it. The pointer IS the durable form,
// so an unpaired worker defers the job back to the enrich spool, using the same
// "not ready yet is never un-enrichable" path a cold model already takes: no
// retry budget consumed, and the sweep picks it up once the pairing lands.
var enrichHold atomic.Pointer[func() bool]

func setEnrichHold(fn func() bool) {
	if fn == nil {
		enrichHold.Store(nil)
		return
	}
	enrichHold.Store(&fn)
}

// enrichHeld reports that enrichment must not consume jobs yet. False whenever
// no hold is installed, so every caller that never sets one is unaffected.
func enrichHeld() bool {
	if fn := enrichHold.Load(); fn != nil {
		return (*fn)()
	}
	return false
}

// holdJob defers one job back to the durable spool because this machine cannot
// publish its result yet. Deliberately NOT a retry: nothing about the job
// failed, so it consumes no attempt and is never quarantined.
func holdJob(key string, p spool.Pointer) {
	if err := spool.Write(p); err != nil {
		log.Printf("keld-agent: job %s held (not paired with Atlas yet) and the spool write failed: %v", key, err)
	}
}
