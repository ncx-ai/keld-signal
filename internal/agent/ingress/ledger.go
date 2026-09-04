package ingress

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

// defaultLedgerLimit/maxLedgerLimit bound the `limit` query parameter: 0 (or
// absent) falls through to ledger.Store's own default, and anything larger
// than maxLedgerLimit is clamped rather than rejected, so a caller cannot
// force an unbounded scan by asking for a huge page.
const maxLedgerLimit = 2000

// LedgerRoute serves GET /v1/ledger?since=<unix>&limit=<n> — the D2
// deliverable's read side (docs/v3/contracts.md). It is registered behind
// the ingress secret like every v3 route (see route.go); this file never
// touches ingress.go or route.go.
func LedgerRoute(r ledger.Reader) Route {
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
			_ = json.NewEncoder(w).Encode(snap)
		})))
	}
}
