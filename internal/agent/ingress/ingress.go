// Package ingress is the daemon's loopback HTTP intake. It accepts pointer or
// inline enrich requests, authenticates with a per-user secret, and enqueues.
package ingress

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"

	"github.com/ncx-ai/keld-signal/internal/agent/queue"
)

type source struct {
	ID      string `json:"id"`
	Origin  string `json:"origin"`
	Version string `json:"version"`
}

type correlation struct {
	Scheme    string `json:"scheme"`
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
}

type pointer struct {
	TranscriptPath string `json:"transcript_path"`
	PromptID       string `json:"prompt_id"`
	Cwd            string `json:"cwd"`
}

type inline struct {
	Text string `json:"text"`
}

// Request is the POST /enrich body.
type Request struct {
	Source      source      `json:"source"`
	Correlation correlation `json:"correlation"`
	Pointer     *pointer    `json:"pointer"`
	Inline      *inline     `json:"inline"`
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
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		j := queue.Job{
			Source:    req.Source.ID,
			Origin:    req.Source.Origin,
			Version:   req.Source.Version,
			Scheme:    req.Correlation.Scheme,
			ID:        req.Correlation.ID,
			SessionID: req.Correlation.SessionID,
		}
		if req.Pointer != nil {
			j.TranscriptPath = req.Pointer.TranscriptPath
			j.Cwd = req.Pointer.Cwd
			j.PromptID = req.Pointer.PromptID
		}
		if req.Inline != nil {
			j.Inline = req.Inline.Text
		}
		if q.Offer(j) {
			w.WriteHeader(http.StatusAccepted)
		} else {
			w.WriteHeader(http.StatusTooManyRequests)
		}
	})
	return mux
}
