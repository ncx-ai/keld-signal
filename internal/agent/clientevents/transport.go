package clientevents

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/ncx-ai/keld-signal/internal/retry"
)

// Transport is the batch-delivery mechanism this package was built around,
// extracted so a SECOND batched-and-spooled path can use it rather than grow a
// second copy of it: POST a marshalled batch, retry transient failures through
// internal/retry, spool to disk when Atlas is unreachable, drop the oldest
// spool files past a cap, and re-post what is spooled on a later sweep.
//
// EXTRACTED, NOT REWRITTEN. Reporter embeds it, so every field a Reporter test
// reaches for (policy, maxSpool, clock, post) still promotes and the event path
// is byte-for-byte the behaviour it was. The signal-embeddings feature path is
// the second user (internal/agent/features): ~200 KB per user per active day,
// an order more than any existing row type, which is precisely the volume that
// makes a bounded buffer plus a drop-oldest spool the right shape and a second
// hand-rolled backoff loop the wrong one.
//
// ⚠️ THE SPOOL IS BOUNDED AND DROPS THE OLDEST, which is only acceptable
// because of what the paths on top of it publish. A client event is ephemeral
// and a dropped one is gone — that is stated and accepted. A feature row is
// re-derivable in principle, but re-deriving one costs an encoder forward pass
// per message, so it is treated as ephemeral too: the cap is what stops a
// laptop that has been offline for a week from filling a disk with vectors.
type Transport struct {
	endpoint string
	token    func() string
	spoolDir string

	policy   retry.Policy
	post     func(ctx context.Context, body []byte) (int, error)
	maxSpool int
	clock    func() time.Time

	httpClient *http.Client

	// onAuthRejection fires at most once per drain when Atlas REJECTS the
	// credential. It is how the daemon's reauther learns to refresh without this
	// package importing it.
	onAuthRejection func()
}

// IsAuthRejection reports whether err is Atlas REJECTING the credential
// (401/403), as opposed to being unavailable or refusing a payload.
//
// ⚠️ THIS IS A THIRD FAILURE CLASS AND IT DID NOT EXIST. retry.IsTransient
// answers a two-way question — retry or not — and puts 401 in "permanent", which
// DrainSpool implements as "delete the batch and carry on". For a rejection both
// halves are wrong: the batch is fine and delivers after a re-onboard, and
// carrying on means learning the same rejection once per spooled file. A laptop
// back from a week offline would turn one revoked token into hundreds of
// requests at the moment an org least wants them.
func IsAuthRejection(err error) bool {
	var se *retry.StatusError
	if errors.As(err, &se) {
		return se.Code == http.StatusUnauthorized || se.Code == http.StatusForbidden
	}
	return false
}

// OnAuthRejection registers the callback fired when a drain hits a rejection.
// Not safe for concurrent use with a running drain; set it at construction.
func (t *Transport) OnAuthRejection(fn func()) { t.onAuthRejection = fn }

// NewTransport builds a Transport posting to endpoint with the ingest token
// from token (a getter, so a later credential rotation — creds.Token.Set — is
// observed without reconstructing anything), spooling failures under spoolDir.
func NewTransport(endpoint string, token func() string, spoolDir string) *Transport {
	t := &Transport{
		endpoint:   endpoint,
		token:      token,
		spoolDir:   spoolDir,
		policy:     retry.DefaultPolicy(),
		maxSpool:   defaultMaxSpool,
		clock:      time.Now,
		httpClient: &http.Client{Timeout: postTimeout},
	}
	t.post = t.doPost
	return t
}

// doPost is the real HTTP POST; swapped out in tests that want to inject a fake
// transport instead of an httptest server.
func (t *Transport) doPost(ctx context.Context, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("x-keld-ingest-token", t.token())
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	// Drain the body so the connection can be reused; the response payload
	// itself carries nothing the caller needs.
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// Deliver POSTs one already-marshalled batch, retrying transient failures, and
// on a final failure spools the batch (transient/exhausted, or a cancelled
// context) or drops it (permanent — retrying can never succeed).
//
// The cancelled-context case is spooled deliberately: retry.Do returns
// context.Canceled, which IsTransient classifies as PERMANENT by design, but a
// batch drained during a shutdown is already out of its buffer and dropping it
// would lose exactly what shutdown prevented us delivering.
func (t *Transport) Deliver(ctx context.Context, body []byte) error {
	if len(body) == 0 {
		return nil
	}
	err := t.postWithRetry(ctx, body)
	if err == nil {
		return nil
	}
	// A rejection is spooled, not dropped: it is fixed by re-onboarding, and the
	// batch is perfectly good.
	if retry.IsTransient(err) || IsAuthRejection(err) || ctx.Err() != nil {
		if spoolErr := t.spool(body); spoolErr != nil {
			return fmt.Errorf("%w (spool failed: %v)", err, spoolErr)
		}
	}
	return err
}

// postWithRetry POSTs body, retrying transient failures per t.policy and
// classifying a non-2xx response through retry.HTTPStatus so the retry loop's
// classifier — and the caller's spool-vs-drop decision — can judge it.
func (t *Transport) postWithRetry(ctx context.Context, body []byte) error {
	return retry.Do(ctx, t.policy, func() error {
		code, err := t.post(ctx, body)
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
// deleting the oldest files (by filename, which sorts ~chronologically thanks
// to the UnixNano prefix) until back within the cap.
func (t *Transport) spool(body []byte) error {
	if err := os.MkdirAll(t.spoolDir, 0o700); err != nil {
		return fmt.Errorf("mkdir spool dir: %w", err)
	}

	name := fmt.Sprintf("%d-%d.json", t.clock().UnixNano(), rand.Int63())
	path := filepath.Join(t.spoolDir, name)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return fmt.Errorf("write spool file: %w", err)
	}

	return t.enforceSpoolCap()
}

// enforceSpoolCap deletes the oldest *.json files in spoolDir until the count
// is within maxSpool.
func (t *Transport) enforceSpoolCap() error {
	files, err := filepath.Glob(filepath.Join(t.spoolDir, "*.json"))
	if err != nil {
		return err
	}
	if len(files) <= t.maxSpool {
		return nil
	}
	sort.Strings(files)
	excess := len(files) - t.maxSpool
	for _, f := range files[:excess] {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("evict spool file %s: %w", f, err)
		}
	}
	return nil
}

// DrainSpool re-posts spooled batches, oldest first. A successfully-posted file
// is removed. On a transient failure the sweep stops immediately, leaving this
// file and any remaining ones for the next sweep (so a down Atlas does not spin
// the sweep in a tight loop). On a permanent failure the poison file is removed
// and the sweep continues. A missing/unreadable spool dir is a no-op.
func (t *Transport) DrainSpool(ctx context.Context) error {
	files, err := filepath.Glob(filepath.Join(t.spoolDir, "*.json"))
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

		postErr := t.postWithRetry(ctx, body)
		if postErr != nil {
			// REJECTED: keep the file and stop. Every remaining batch would be
			// told the same thing, and re-sending one with a token already known
			// to be rejected is the burst this guards against.
			if IsAuthRejection(postErr) {
				if t.onAuthRejection != nil {
					t.onAuthRejection()
				}
				return nil
			}
			// UNAVAILABLE: keep the file and end this sweep — the same condition
			// would meet every remaining batch. Never a re-onboard.
			if retry.IsTransient(postErr) {
				return nil
			}
			// REFUSED: this payload is bad, the connection is fine. Drop it and
			// CONTINUE, or one malformed batch blocks every good one behind it.
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
