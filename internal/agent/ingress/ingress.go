// Package ingress is the daemon's loopback HTTP intake. It accepts pointer or
// inline enrich requests, authenticates with a per-user secret, and enqueues.
package ingress

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

// JobFrom builds the queue.Job for an enrich pointer. Shared by the HTTP handler
// and the daemon's spool drain so both paths enqueue identically.
func JobFrom(p spool.Pointer) queue.Job {
	j := queue.Job{
		Source:    p.Source.ID,
		Origin:    p.Source.Origin,
		Version:   p.Source.Version,
		Scheme:    p.Correlation.Scheme,
		ID:        p.Correlation.ID,
		SessionID: p.Correlation.SessionID,
	}
	if p.Pointer != nil {
		j.TranscriptPath = p.Pointer.TranscriptPath
		j.Cwd = p.Pointer.Cwd
		j.PromptID = p.Pointer.PromptID
	}
	if p.Inline != nil {
		j.Inline = p.Inline.Text
	}
	return j
}

// Handler returns the daemon's HTTP handler. extras are the v3 loopback routes
// (/v1/ledger, /v1/settings, /v1/projects, /v1/config, the page) — each lane
// contributes its own Route from its own file, so nobody edits this one.
func Handler(q *queue.Queue, secret string, extras ...Route) http.Handler {
	mux := http.NewServeMux()
	mount(mux, secret, extras)
	mux.HandleFunc("/enrich", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("x-keld-agent-secret")), []byte(secret)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB cap
		var p spool.Pointer
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// 202 for Duplicate as well as Accepted: both mean the daemon has taken
		// the prompt on. 429 is reserved for real backpressure, because the hook
		// treats any >=400 as failure and durably spools the pointer — so
		// answering 429 to a dedup makes it spool work that is already done.
		// The hook/watcher overlap that produces duplicates is designed
		// (queue.Complete exists for it) and must not read as overload.
		if q.Offer(JobFrom(p)).TakenOn() {
			w.WriteHeader(http.StatusAccepted)
		} else {
			w.WriteHeader(http.StatusTooManyRequests)
		}
	})
	return mux
}

// OnPointer observes every /enrich pointer the daemon ACCEPTS, before it is
// enqueued or discarded. It exists so the integrations pane can answer "did this
// tool's hook fire", which is a fact about the TOOL and stays true whether or not
// this daemon goes on to enrich the prompt.
//
// ⚠️ It is called from DiscardHandler too, and that is the whole point. Under
// `ml_backend: "off"` no enrichment worker runs, so the worker's own call site
// never fires — while telemetry is explicitly unaffected by that mode and keeps
// reporting. One expected lane active and another silent is the predicate for
// `broken`, so every machine with enrichment off would have been reported broken
// on a hook that fired correctly every time.
//
// Never called for a rejected request: an unauthenticated or malformed POST
// establishes nothing about the tool, and recording it would make the lane
// unfalsifiable. A seam rather than a parameter because DiscardHandler has ~20
// call sites, and observation is not part of its contract.
var OnPointer func(spool.Pointer)

// notePointer calls OnPointer if one is set. Panic-isolated: an observer is a
// reporting concern and must never turn an accepted pointer into a 500.
func notePointer(p spool.Pointer) {
	f := OnPointer
	if f == nil {
		return
	}
	defer func() { _ = recover() }()
	f(p)
}

// DiscardHandler returns the daemon's /enrich handler for when enrichment is
// disabled (ml_backend=off): it authenticates and validates the request body
// exactly like Handler, but never enqueues — it accepts-and-discards (202) so
// the hook does not spool pointers that would otherwise never be processed.
func DiscardHandler(secret string, extras ...Route) http.Handler {
	mux := http.NewServeMux()
	mount(mux, secret, extras)
	mux.HandleFunc("/enrich", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("x-keld-agent-secret")), []byte(secret)) != 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB cap
		var p spool.Pointer
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// The hook fired even though nothing will consume this pointer.
		notePointer(p)
		w.WriteHeader(http.StatusAccepted)
	})
	return mux
}
