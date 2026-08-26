package features

import (
	"context"
	"log"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
)

// DefaultFlush is how often the reporter empties the emitter's buffer.
//
// It is LATENCY ONLY, and generous on purpose. A feature row is training data
// paired at Keld with a `y` derived hours later — nothing acts on one, nothing
// alerts on one, and no human is waiting for one. Flushing every 30 seconds
// like the client-events reporter would mean a POST carrying two rows, which
// wastes exactly the batching this path exists to get. Two minutes against a
// five-minute sweep means most flushes carry a whole sweep's take.
const DefaultFlush = 2 * time.Minute

// EnvFlush overrides the flush interval.
const EnvFlush = "KELD_FEATURES_FLUSH"

// Deliverer is the transport a Reporter delivers through:
// clientevents.Transport, declared as an interface so a test can observe the
// bodies without an httptest server and so nothing here can reach past
// Deliver/DrainSpool into the retry policy.
type Deliverer interface {
	Deliver(ctx context.Context, body []byte) error
	DrainSpool(ctx context.Context) error
}

// Reporter drains the emitter's buffered rows on a timer, chunks them, and
// hands each chunk to the transport.
//
// ⚠️ IT REUSES clientevents' TRANSPORT RATHER THAN REPEATING IT. Bounded
// buffer, periodic flush, internal/retry with the canonical IsTransient
// classifier, drop-oldest spool under ~/.keld/spool — that mechanism already
// exists, was already got right once, and a second hand-rolled backoff loop is
// the thing this repo's conventions name explicitly. What is different here is
// only the envelope and the volume.
//
// The PUBLISH GATE is read per flush, not captured at construction, for the
// reason the emitter reads its own gate per sweep: the org's override arrives
// on the first settings poll, which lands after wiring. With the gate closed
// the reporter still DRAINS — a buffer nobody empties would wedge the emitter's
// backpressure and stop it collecting at all — and DISCARDS. That is the honest
// reading of a `publish` toggle that is separate from `features`: collecting
// locally is what `features` bought, sending is what `publish` buys, and the
// two are independent by design.
type Reporter struct {
	tr        Deliverer
	drain     func() []publish.FeatureRow
	installID string
	gate      func() bool
}

// NewReporter builds a Reporter over the given transport. gate may be nil,
// which reads as "publish".
func NewReporter(tr Deliverer, drain func() []publish.FeatureRow, installID string, gate func() bool) *Reporter {
	return &Reporter{tr: tr, drain: drain, installID: installID, gate: gate}
}

// Run drains the spool left over from a previous process, then flushes on
// interval. On cancellation it makes one best-effort final flush on a detached
// context, so rows already collected are not lost to a graceful shutdown — the
// same shape clientevents.Reporter.Run uses.
func (r *Reporter) Run(ctx context.Context, interval time.Duration) {
	if err := r.tr.DrainSpool(ctx); err != nil {
		log.Printf("keld-agent: feature reporter startup drainSpool: %v", err)
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			if err := r.Flush(ctx); err != nil {
				log.Printf("keld-agent: feature reporter flush: %v", err)
			}
			if err := r.tr.DrainSpool(ctx); err != nil {
				log.Printf("keld-agent: feature reporter drainSpool: %v", err)
			}
		case <-ctx.Done():
			shutdown, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := r.Flush(shutdown); err != nil {
				log.Printf("keld-agent: feature reporter shutdown flush: %v", err)
			}
			cancel()
			return
		}
	}
}

// Flush drains the buffer and delivers it in chunks of batchRows.
//
// ⚠️ IT STOPS AT THE FIRST FAILING CHUNK and DROPS the rest, and that is not
// the block emitter's rule. There, a failed batch is re-fetched from the store
// next interval, because the cursor is what asks for it again. Here the cursor
// has already moved: these rows are in hand and nowhere else, so the transport
// is the only thing that can preserve them — which it does, by spooling the
// chunk it could not deliver. Continuing past a failure would push chunk after
// chunk into a spool that is already dropping its oldest entries, turning one
// outage into an eviction storm; the next flush carries the next take instead.
//
// A chunk that will not even marshal is skipped and counted rather than
// aborting the flush: one malformed row must not strand the rows behind it.
func (r *Reporter) Flush(ctx context.Context) error {
	rows := r.drain()
	if len(rows) == 0 {
		return nil
	}
	// The gate is checked AFTER the drain, deliberately. Draining regardless is
	// what keeps the emitter's backpressure from wedging while publishing is
	// off; see the type comment.
	if r.gate != nil && !r.gate() {
		return nil
	}
	for start := 0; start < len(rows); start += batchRows {
		end := start + batchRows
		if end > len(rows) {
			end = len(rows)
		}
		body, err := publish.MarshalFeatures(r.installID, rows[start:end])
		if err != nil {
			log.Printf("keld-agent: feature batch not marshalable, dropped %d rows: %v",
				end-start, err)
			continue
		}
		if err := r.tr.Deliver(ctx, body); err != nil {
			return err
		}
	}
	return nil
}

// NewTransport builds the delivery half against the features route. Split out
// so the wiring reads as one line and so a caller cannot accidentally point the
// feature spool at the client-events spool directory — two paths sharing a
// spool dir would re-post each other's bodies to each other's endpoints.
func NewTransport(endpoint string, token func() string, spoolDir string) *clientevents.Transport {
	return clientevents.NewTransport(endpoint, token, spoolDir)
}
