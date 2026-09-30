package daemon

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

// GET /v1/usage — the requests table summed per 5-minute bucket (the contract
// is docs/v3/contracts.md → `GET /v1/usage`). The page reads its tokens and
// spend from here and joins each bucket to a block for repo and projects.

type usageTokensWire struct {
	Input         int64 `json:"input"`
	Output        int64 `json:"output"`
	CacheRead     int64 `json:"cache_read"`
	CacheCreation int64 `json:"cache_creation"`
}

type usageBucketWire struct {
	At          int64           `json:"at"`
	Source      string          `json:"source"`
	Transcript  string          `json:"transcript"`
	Model       string          `json:"model"`
	Requests    int64           `json:"requests"`
	Tokens      usageTokensWire `json:"tokens"`
	EstimateUSD float64         `json:"estimate_usd"`
}

type usageSourceWire struct {
	FirstAt int64 `json:"first_at"`
}

type usageWire struct {
	Since         int64                      `json:"since"`
	Until         int64                      `json:"until"`
	BucketSeconds int                        `json:"bucket_seconds"`
	BackfillDone  bool                       `json:"backfill_done"`
	Sources       map[string]usageSourceWire `json:"sources"`
	Buckets       []usageBucketWire          `json:"buckets"`
}

func usageRoute(store *ledger.Store) ingress.Route {
	return func(mux *http.ServeMux, auth func(http.Handler) http.Handler) {
		mux.Handle("/v1/usage", auth(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if req.Method != http.MethodGet {
				w.WriteHeader(http.StatusMethodNotAllowed)
				return
			}
			since, err1 := strconv.ParseInt(req.URL.Query().Get("since"), 10, 64)
			until, err2 := strconv.ParseInt(req.URL.Query().Get("until"), 10, 64)
			if err1 != nil || err2 != nil || until <= since {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			buckets, err := store.UsageBuckets(time.Unix(since, 0), time.Unix(until, 0))
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			first, err := store.FirstRequestAt()
			if err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			out := usageWire{
				Since: since, Until: until, BucketSeconds: ledger.UsageBucketSeconds,
				BackfillDone: store.RequestsBackfillDone(),
				Sources:      map[string]usageSourceWire{},
				Buckets:      make([]usageBucketWire, 0, len(buckets)),
			}
			for src, at := range first {
				out.Sources[src] = usageSourceWire{FirstAt: at.Unix()}
			}
			for _, b := range buckets {
				out.Buckets = append(out.Buckets, usageBucketWire{
					At: b.At.Unix(), Source: b.Source, Transcript: b.Transcript, Model: b.Model,
					Requests: b.Requests,
					Tokens: usageTokensWire{Input: b.Input, Output: b.Output,
						CacheRead: b.CacheRead, CacheCreation: b.CacheCreation},
					EstimateUSD: b.EstimateUSD,
				})
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(out)
		})))
	}
}
