package clientevents

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// defaultMaxSpool bounds how many spooled batches accumulate on disk while
// Atlas is unreachable; oldest files are dropped once the cap is exceeded.
const defaultMaxSpool = 256

// postTimeout bounds a single POST attempt so a hung connection can't wedge a
// retry attempt (and, transitively, the reporter loop) indefinitely.
const postTimeout = 30 * time.Second

// envelope is the wire format posted to Atlas: a versioned batch of events
// plus the install id that produced them.
type envelope struct {
	SchemaVersion int     `json:"schema_version"`
	InstallID     string  `json:"install_id"`
	Events        []Event `json:"events"`
}

// Reporter drains buffered client events from an Emitter (via the injected
// drain func) on a timer, wraps them in a versioned envelope, and POSTs them
// to Atlas. Transient failures are retried (internal/retry); a batch that
// still fails after retries is spooled to disk for a later DrainSpool sweep
// so events survive Atlas being down. A batch that fails permanently (e.g. a
// 400/401) is dropped — re-posting it will never succeed.
//
// The POST/retry/spool machinery is EMBEDDED rather than owned: Transport is
// exactly that code, extracted so the signal-embeddings feature path could
// reuse it instead of growing a second copy of a backoff-and-spool loop.
// Embedded anonymously (not a named field) so every promoted field the tests
// and the methods here already reach for — policy, maxSpool, clock, post —
// keeps its spelling and the event path's behaviour is unchanged.
type Reporter struct {
	*Transport

	installID string
	drain     func() []Event
}

// NewReporter builds a Reporter that POSTs drained batches to endpoint,
// calling token on every POST for the x-keld-ingest-token credential
// (matching the publish/settings client<->Atlas convention — a getter so a
// later credential rotation, e.g. creds.Token.Set, is observed without
// reconstructing the Reporter), tagging the envelope with installID. Spooled
// batches (written when Atlas is unreachable) live under spoolDir.
func NewReporter(endpoint string, token func() string, installID string, drain func() []Event, spoolDir string) *Reporter {
	return &Reporter{
		Transport: NewTransport(endpoint, token, spoolDir),
		installID: installID,
		drain:     drain,
	}
}

// Run drains any spooled batches left over from a previous run, then loops on
// interval calling flush followed by DrainSpool. On context cancellation it
// performs one best-effort final flush (using a short-lived detached context,
// since the passed ctx is already cancelled) so buffered events aren't lost
// on graceful shutdown, then returns.
func (r *Reporter) Run(ctx context.Context, interval time.Duration) {
	if err := r.DrainSpool(ctx); err != nil {
		log.Printf("clientevents: reporter startup drainSpool: %v", err)
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := r.flush(ctx); err != nil {
				log.Printf("clientevents: reporter flush: %v", err)
			}
			if err := r.DrainSpool(ctx); err != nil {
				log.Printf("clientevents: reporter drainSpool: %v", err)
			}
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), postTimeout)
			if err := r.flush(shutdownCtx); err != nil {
				log.Printf("clientevents: reporter shutdown flush: %v", err)
			}
			cancel()
			return
		}
	}
}

// flush drains the current batch, POSTs it (retrying transient failures), and
// on a final failure spools the batch (transient/exhausted, or a context
// cancelled mid-flight) or drops it (permanent — retrying would never
// succeed). A nil/empty drain is a no-op: no POST is made.
//
// The spool-vs-drop decision lives in Transport.Deliver rather than here, so
// the feature path cannot make a different one by accident; see its comment
// for why a cancelled context spools despite IsTransient calling it permanent.
func (r *Reporter) flush(ctx context.Context) error {
	events := r.drain()
	if len(events) == 0 {
		return nil
	}

	env := envelope{SchemaVersion: SchemaVersion, InstallID: r.installID, Events: events}
	body, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("clientevents: marshal envelope: %w", err)
	}
	return r.Deliver(ctx, body)
}
