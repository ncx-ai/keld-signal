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

// Handler returns the daemon's HTTP handler.
func Handler(q *queue.Queue, secret string) http.Handler {
	mux := http.NewServeMux()
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
		if q.Offer(JobFrom(p)) {
			w.WriteHeader(http.StatusAccepted)
		} else {
			w.WriteHeader(http.StatusTooManyRequests)
		}
	})
	return mux
}
