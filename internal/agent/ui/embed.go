// Package ui is the Keld Signal page — deliverable D6 of docs/v3/contracts.md:
// one embedded, framework-free HTML/CSS/JS app with three panes (Today,
// Projects, Settings) rendering exactly the JSON shapes that document defines
// for GET /v1/ledger, GET /v1/settings and GET /v1/projects.
//
// This package owns only the page. The daemon wiring
// that serves those routes for real, and mounting Route() on the live daemon
// mux, is phase 2 (other lanes' work per docs/v3/contracts.md's plan) — this
// package never imports the daemon and never talks to Atlas, the sidecar or
// the ledger store directly. It reads only the wire shapes.
package ui

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
)

// assets is the whole page. No build step and no framework, per the D6 scope
// in docs/v3/contracts.md: one HTML file, one stylesheet, one script.
//
//go:embed index.html app.css app.js
var assets embed.FS

const (
	// secretQueryParam is the query parameter the page's own link carries the
	// daemon's per-user secret in, once. docs/v3/contracts.md documents the
	// cookie the secret ends up in (keld_secret, read by
	// internal/agent/ingress.RequireSecret) but not how the page's first load
	// receives it — this name is this lane's choice, not specified upstream.
	// Whoever wires `keld signal open` (D6b) must build the URL as
	// "http://127.0.0.1:<port>/?secret=<the secret>".
	secretQueryParam = "secret"
	secretCookie     = "keld_secret" // name ingress.RequireSecret already reads
)

// Route registers the page at "/". Per the D6 scope, the static assets carry
// no data about this machine and are therefore NOT wrapped in the auth
// middleware — only /v1/ledger, /v1/settings, /v1/projects and /v1/config
// (each some other lane's own Route) are. The page takes the secret once from
// its own URL's query parameter and sets it as the keld_secret cookie, so
// every fetch after the first load carries it without the query parameter
// ever appearing again.
func Route() ingress.Route {
	return func(mux *http.ServeMux, _ func(http.Handler) http.Handler) {
		mux.Handle("/", withSecretCookie(assetHandler()))
	}
}

func assetHandler() http.Handler {
	sub, err := fs.Sub(assets, ".")
	if err != nil {
		// Only reachable if the go:embed directive above stops matching a
		// file in this directory — a build-time mistake, not a runtime one.
		panic("ui: embedded assets: " + err.Error())
	}
	return http.FileServer(http.FS(sub))
}

func withSecretCookie(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s := r.URL.Query().Get(secretQueryParam); s != "" {
			http.SetCookie(w, &http.Cookie{
				Name:     secretCookie,
				Value:    s,
				Path:     "/",
				SameSite: http.SameSiteLaxMode,
				// Not HttpOnly: app.js never reads it back (the browser
				// attaches it to every same-origin fetch on its own), and
				// marking it HttpOnly buys nothing here.
			})
		}
		next.ServeHTTP(w, r)
	})
}
