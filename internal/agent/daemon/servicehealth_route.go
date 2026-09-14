package daemon

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

// errNoServiceToRestart is the restart route's "there is nothing here to
// restart" — no sidecar is installed, or enrichment is off. It is a 409, not a
// 500 and not a silent 202: answering 202 would tell the page a restart is
// under way when nothing will ever happen, which is the confident-wrong-answer
// failure this whole branch exists to remove.
var errNoServiceToRestart = errors.New("no analysis service to restart")

// serviceWire is the `service` object on GET /v1/ledger. Field names are the
// UI contract and are pinned by a test:
//
//	"service": {"state": "...", "reason": "...", "failures": 0}
//
// ⚠️ **IT IS A SIBLING OF `health`, NEVER A MEMBER OF IT.** `health` is the
// PIPELINE's per-stage table (cut / measured / attributed / sent / received,
// plus the machine-level rows). "Is the analysis service running" is a
// different question, and merging them would let a block legitimately pending
// delivery render as a dead sidecar, and a dead sidecar hide inside a row about
// blocks.
//
// ⚠️ **AND IT IS ALWAYS EMITTED, INCLUDING WHEN `ok`.** An absent key must mean
// "this daemon is too old to say", never "fine" — the same refusal the rest of
// this codebase makes about a check that did not run.
type serviceWire struct {
	// State is one of ok | degraded | restarting | stuck | not_applicable.
	State string `json:"state"`
	// Reason is a human sentence, empty only when State is ok. On `stuck` it
	// must say what was already TRIED (see stuckReason).
	Reason string `json:"reason"`
	// Failures is the count of CONSECUTIVE unanswered health probes, reset to 0
	// on any success.
	//
	// ⚠️ **IT IS THE SAME COUNTER THE 3-AND-6 RUNGS FIRE ON, not a parallel
	// tally kept for display.** A second count would drift from the one that
	// decides, and the page would then be reporting a number that does not
	// explain the actions the machine took. There is exactly one integer
	// (serviceHealth.fails), incremented in onFailure and zeroed in onSuccess,
	// and both the ladder and this field read it.
	//
	// It is 0 during the startup grace even while State is `degraded` — no
	// streak has begun, and the reason sentence carries that.
	Failures int `json:"failures"`
}

// ledgerEnvelope is the GET /v1/ledger body: the ledger's own Snapshot, with
// the service block beside it.
//
// The Snapshot is EMBEDDED, so its fields inline in their existing declaration
// order and the shape internal/agent/ledger owns cannot drift from what is
// served here — the compiler carries any change across. `service` marshals
// last, after `pending`.
type ledgerEnvelope struct {
	ledger.Snapshot
	Service serviceWire `json:"service"`
}

// maxLedgerLimit mirrors internal/agent/ingress's own clamp on `limit`, so a
// caller cannot force an unbounded scan by asking for a huge page.
//
// ⚠️ This route replaces ingress.LedgerRoute rather than wrapping it, because
// there is no seam in the ledger package for a key it does not own: Snapshot is
// a struct, Reader returns one, and neither has an extension point. Composing
// the page's payload is the daemon's job — it is the only layer that knows both
// the ledger and the sidecar. ingress.LedgerRoute is left in place, still
// tested, and is what a caller with no service health would mount.
const maxLedgerLimit = 2000

// ledgerRoute serves GET /v1/ledger?since=<unix>&limit=<n> plus the `service`
// key. svc is read per request so a route mounted before the health owner
// exists (the onboarding handler does exactly that) still answers.
func ledgerRoute(r ledger.Reader, svc func() serviceWire) ingress.Route {
	return func(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
		mux.Handle("/v1/ledger", auth(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}

			since := time.Time{}
			if v := req.URL.Query().Get("since"); v != "" {
				n, err := strconv.ParseInt(v, 10, 64)
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				since = time.Unix(n, 0)
			}

			limit := 0
			if v := req.URL.Query().Get("limit"); v != "" {
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				limit = n
			}
			if limit > maxLedgerLimit {
				limit = maxLedgerLimit
			}

			snap, err := r.Read(since, limit)
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(ledgerEnvelope{Snapshot: snap, Service: svc()})
		})))
	}
}

// defaultRestartMinInterval is the restart button's rate limit.
//
// ⚠️ **A BUTTON A PERSON CAN HOLD DOWN IS A RESTART LOOP WITH A HUMAN IN IT.**
// A sidecar restart costs a process spawn and, on the next inference, a model
// load; issuing them faster than the service can come up produces exactly the
// never-ready machine the ladder above is trying to fix. 30s is comfortably
// longer than a measured clean stop (110.6 ms) plus a cold start, so a genuine
// second attempt is never refused while a double-click always is.
const defaultRestartMinInterval = 30 * time.Second

// restartLimiter is the token-of-one rate limit on POST /v1/service/restart.
type restartLimiter struct {
	mu       sync.Mutex
	last     time.Time
	min      time.Duration
	nowFunc  func() time.Time
	accepted int
}

func newRestartLimiter() *restartLimiter {
	return &restartLimiter{
		min:     envDuration("KELD_SERVICE_RESTART_MIN_INTERVAL", defaultRestartMinInterval),
		nowFunc: time.Now,
	}
}

// allow reports whether a restart may proceed now, and if not, how long the
// caller must wait. It records the acceptance, so two concurrent requests
// cannot both pass.
func (l *restartLimiter) allow() (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.nowFunc()
	if !l.last.IsZero() {
		if wait := l.min - now.Sub(l.last); wait > 0 {
			return wait, false
		}
	}
	l.last = now
	l.accepted++
	return 0, true
}

// serviceRestartRoute serves POST /v1/service/restart.
//
// ⚠️ **202, AND IT MUST NOT BLOCK ON THE RESTART COMPLETING.** The stop half
// alone is bounded by KELD_SIDECAR_STOP_GRACE (5s) and the start half by the
// supervisor's 30s ready timeout; a handler that waited would hold a page
// request open for the whole of that and time out in the browser before the
// service came back — reporting a failure for a restart that worked. So the
// answer is "accepted", and the page learns the outcome from the `service`
// block on its next /v1/ledger poll, which is the surface that actually knows.
//
// Authentication is the shared per-user secret every other loopback route uses
// (ingress.RequireSecret, applied by the `auth` wrapper this Route is handed) —
// the same check /enrich has always done, so this cannot get it subtly
// different.
func serviceRestartRoute(restart func() error, lim *restartLimiter) ingress.Route {
	if lim == nil {
		lim = newRestartLimiter()
	}
	return func(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
		mux.Handle("/v1/service/restart", auth(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != http.MethodPost {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			if wait, ok := lim.allow(); !ok {
				w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
				writeJSON(w, http.StatusTooManyRequests, map[string]any{
					"error":         "rate_limited",
					"retry_after_s": int(wait.Seconds()) + 1,
				})
				return
			}
			// The work goes to its own goroutine and the response goes out now.
			// RequestRestart is itself a non-blocking channel send, but that is
			// a property of today's implementation and this contract says
			// "never blocks" — so it is enforced here rather than depended on
			// one layer down.
			done := make(chan error, 1)
			go func() { done <- restart() }()
			select {
			case err := <-done:
				if errors.Is(err, errNoServiceToRestart) {
					writeJSON(w, http.StatusConflict, map[string]any{
						"error":  "not_applicable",
						"detail": "there is no analysis service on this machine to restart",
					})
					return
				}
				if err != nil {
					// The supervisor has surrendered (ErrSupervisorStopped) or
					// refused. Said out loud rather than reported as accepted:
					// the remedy is a daemon restart, and the `service` block
					// says so.
					writeJSON(w, http.StatusConflict, map[string]any{
						"error":  "cannot_restart",
						"detail": "the analysis service could not be restarted; its supervisor is no longer running",
					})
					return
				}
			case <-time.After(250 * time.Millisecond):
				// Longer than a channel send should ever take, so whatever is
				// happening is not going to resolve inside this request:
				// answer accepted rather than hold it. The goroutine finishes
				// on its own and the ladder reports the outcome on the page's
				// next poll. The only errors that matter here (no service,
				// surrendered supervisor) return immediately.
			}
			writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
		})))
	}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
