package clientevents

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ncx-ai/keld-signal/internal/retry"
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
// still fails after retries is spooled to disk for a later drainSpool sweep
// so events survive Atlas being down. A batch that fails permanently (e.g. a
// 400/401) is dropped — re-posting it will never succeed.
type Reporter struct {
	endpoint  string
	token     string
	installID string
	drain     func() []Event
	spoolDir  string

	policy   retry.Policy
	post     func(ctx context.Context, body []byte) (int, error)
	maxSpool int
	clock    func() time.Time

	httpClient *http.Client
}

// NewReporter builds a Reporter that POSTs drained batches to endpoint using
// token as the x-keld-ingest-token credential (matching the publish/settings
// client<->Atlas convention), tagging the envelope with installID. Spooled
// batches (written when Atlas is unreachable) live under spoolDir.
func NewReporter(endpoint, token, installID string, drain func() []Event, spoolDir string) *Reporter {
	r := &Reporter{
		endpoint:   endpoint,
		token:      token,
		installID:  installID,
		drain:      drain,
		spoolDir:   spoolDir,
		policy:     retry.DefaultPolicy(),
		maxSpool:   defaultMaxSpool,
		clock:      time.Now,
		httpClient: &http.Client{Timeout: postTimeout},
	}
	r.post = r.doPost
	return r
}

// doPost is the real HTTP POST implementation; swapped out in tests that want
// to inject a fake transport instead of an httptest server.
func (r *Reporter) doPost(ctx context.Context, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("x-keld-ingest-token", r.token)
	req.Header.Set("Content-Type", "application/json")

	resp, err := r.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// Drain the body so the connection can be reused; the response payload
	// itself carries nothing the reporter needs.
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// Run drains any spooled batches left over from a previous run, then loops on
// interval calling flush followed by drainSpool. On context cancellation it
// performs one best-effort final flush (using a short-lived detached context,
// since the passed ctx is already cancelled) so buffered events aren't lost
// on graceful shutdown, then returns.
func (r *Reporter) Run(ctx context.Context, interval time.Duration) {
	if err := r.drainSpool(ctx); err != nil {
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
			if err := r.drainSpool(ctx); err != nil {
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
// on a final failure spools the batch (transient/exhausted) or drops it
// (permanent — retrying would never succeed). A nil/empty drain is a no-op:
// no POST is made.
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

	postErr := r.postWithRetry(ctx, body)
	if postErr != nil {
		// Spool when the failure is transient/exhausted OR when the context was
		// cancelled mid-flight (daemon shutting down while Atlas is slow/down):
		// retry.Do returns context.Canceled, which IsTransient classifies as
		// PERMANENT by design — but this batch is already drained, so dropping
		// it would silently lose events that shutdown prevented us delivering.
		// Preserve it for the next process start instead.
		if retry.IsTransient(postErr) || ctx.Err() != nil {
			if spoolErr := r.spool(body); spoolErr != nil {
				log.Printf("clientevents: spool write failed: %v", spoolErr)
			}
		}
		// Otherwise a permanent failure (e.g. 400/401) is poison: re-posting can
		// never succeed, so the batch is dropped rather than spooled.
		return postErr
	}
	return nil
}

// postWithRetry POSTs body, retrying transient failures per r.policy, and
// classifying a non-2xx response via retry.HTTPStatus so the retry loop's
// classifier (and the caller's spool-vs-drop decision) can judge it.
func (r *Reporter) postWithRetry(ctx context.Context, body []byte) error {
	return retry.Do(ctx, r.policy, func() error {
		code, err := r.post(ctx, body)
		if err != nil {
			return err
		}
		if code < 200 || code >= 300 {
			return retry.HTTPStatus(code)
		}
		return nil
	})
}

// spool writes body to a new file under spoolDir, then enforces maxSpool by
// deleting the oldest files (by filename, which sorts ~chronologically
// thanks to the UnixNano prefix) until back within the cap.
func (r *Reporter) spool(body []byte) error {
	if err := os.MkdirAll(r.spoolDir, 0o700); err != nil {
		return fmt.Errorf("mkdir spool dir: %w", err)
	}

	name := fmt.Sprintf("%d-%d.json", r.clock().UnixNano(), rand.Int63())
	path := filepath.Join(r.spoolDir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write spool file: %w", err)
	}

	return r.enforceSpoolCap()
}

// enforceSpoolCap deletes the oldest *.json files in spoolDir until the count
// is within maxSpool.
func (r *Reporter) enforceSpoolCap() error {
	files, err := filepath.Glob(filepath.Join(r.spoolDir, "*.json"))
	if err != nil {
		return err
	}
	if len(files) <= r.maxSpool {
		return nil
	}
	sort.Strings(files)
	excess := len(files) - r.maxSpool
	for _, f := range files[:excess] {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("evict spool file %s: %w", f, err)
		}
	}
	return nil
}

// drainSpool re-posts spooled batches, oldest first. A successfully-posted
// file is removed. On a transient failure the sweep stops immediately,
// leaving this file and any remaining ones for the next sweep (so a down
// Atlas doesn't spin the sweep in a tight loop). On a permanent failure the
// poison file is removed and the sweep continues with the next one. A
// missing/unreadable spool dir is a no-op.
func (r *Reporter) drainSpool(ctx context.Context) error {
	files, err := filepath.Glob(filepath.Join(r.spoolDir, "*.json"))
	if err != nil || len(files) == 0 {
		return nil
	}
	sort.Strings(files)

	for _, f := range files {
		body, readErr := os.ReadFile(f)
		if readErr != nil {
			if os.IsNotExist(readErr) {
				continue
			}
			return fmt.Errorf("read spool file %s: %w", f, readErr)
		}

		postErr := r.postWithRetry(ctx, body)
		if postErr != nil {
			if retry.IsTransient(postErr) {
				return nil
			}
			if rmErr := os.Remove(f); rmErr != nil && !os.IsNotExist(rmErr) {
				return fmt.Errorf("remove poison spool file %s: %w", f, rmErr)
			}
			continue
		}

		if rmErr := os.Remove(f); rmErr != nil && !os.IsNotExist(rmErr) {
			return fmt.Errorf("remove posted spool file %s: %w", f, rmErr)
		}
	}
	return nil
}
