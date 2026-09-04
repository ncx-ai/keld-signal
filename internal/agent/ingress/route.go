package ingress

import (
	"crypto/subtle"
	"net/http"
)

// Route is one v3 loopback surface, registered on the daemon's mux by
// Handler/DiscardHandler. It is a SEAM (docs/v3/contracts.md, D0): the ledger,
// settings, projects, config and page lanes each ship a Route from their own
// package and never touch ingress.go.
//
// Register receives the mux and an auth wrapper. Every route that returns
// anything about this machine MUST wrap itself with auth — the only exceptions
// are the page's static assets, which carry no data (the page fetches data
// through authed routes with the secret it was handed once, see the ui lane).
type Route func(mux *http.ServeMux, auth func(http.Handler) http.Handler)

// RequireSecret is the per-user-secret check /enrich has always done, as a
// middleware so the v3 routes cannot get it subtly different. A missing or
// wrong secret is 401 with no body. Accepted in the header AI tooling already
// uses (x-keld-agent-secret) and, for the page, as the cookie the page sets
// from its one-time query parameter.
func RequireSecret(secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := r.Header.Get("x-keld-agent-secret")
			if got == "" {
				if c, err := r.Cookie("keld_secret"); err == nil {
					got = c.Value
				}
			}
			if subtle.ConstantTimeCompare([]byte(got), []byte(secret)) != 1 || secret == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func mount(mux *http.ServeMux, secret string, extras []Route) {
	auth := RequireSecret(secret)
	for _, r := range extras {
		if r != nil {
			r(mux, auth)
		}
	}
}
