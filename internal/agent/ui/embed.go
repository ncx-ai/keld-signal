// Package ui is the Keld Signal page — deliverable D6 of docs/v3/contracts.md:
// one embedded, framework-free HTML/CSS/JS app with three panes (Today,
// Projects, Settings) rendering exactly the JSON shapes that document defines
// for GET /v1/ledger, GET /v1/settings and GET /v1/projects.
//
// This package owns only the page and its own dev server. The daemon wiring
// that serves those routes for real, and mounting Route() on the live daemon
// mux, is phase 2 (other lanes' work per docs/v3/contracts.md's plan) — this
// package never imports the daemon and never talks to Atlas, the sidecar or
// the ledger store directly. It reads only the wire shapes.
package ui

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"

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

// DevServer is lane D's own tool, never linked into the daemon build (see
// cmd/ui-dev): it serves the same embedded page at "/" plus /v1/ledger,
// /v1/settings and /v1/projects from static JSON files under fixturesDir
// (ledger.json, settings.json, projects.json), with NO auth check at all, so
// the page can be built and reviewed against docs/v3/contracts.md's shapes
// without a daemon, a sidecar or a secret.
//
// A fixture file that does not exist answers 503 — which is also,
// deliberately, how the "daemon not running" fixture works: point
// --fixtures at an empty (or missing) directory and every route 503s exactly
// as a real daemon that isn't running would refuse the connection, and the
// page's own offline handling (app.js) takes over from there.
//
// Mutating routes (PUT /v1/settings, POST /v1/projects/..., PUT
// /v1/workstreams/.../off, POST /v1/config) are accepted and echoed back
// rather than persisted: this is a fixture reviewer, not a second
// implementation of D2/D3/D4's validation, restart bookkeeping or 409 rules.
func DevServer(fixturesDir string) http.Handler {
	mux := http.NewServeMux()
	// Same cookie-setting wrapper Route() uses in production, so
	// http://127.0.0.1:8899/?secret=anything exercises the real "take the
	// secret once, remember it as a cookie" flow locally too — DevServer
	// itself checks no secret at all, but the page's own behavior should
	// look identical whichever server is behind it.
	mux.Handle("/", withSecretCookie(assetHandler()))

	serveFixture := func(filename string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			b, err := os.ReadFile(filepath.Join(fixturesDir, filename))
			if err != nil {
				http.Error(w, `{"error":"daemon_not_running"}`, http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
		}
	}

	mux.HandleFunc("GET /v1/ledger", serveFixture("ledger.json"))
	mux.HandleFunc("GET /v1/settings", serveFixture("settings.json"))
	mux.HandleFunc("GET /v1/projects", serveFixture("projects.json"))

	mux.HandleFunc("PUT /v1/settings", devEcho(map[string]any{"restart_required": false}))
	mux.HandleFunc("POST /v1/projects/bundle", devEcho(map[string]any{"ok": true}))
	mux.HandleFunc("POST /v1/projects/place", devEcho(map[string]any{"ok": true}))
	mux.HandleFunc("POST /v1/projects/", devEcho(map[string]any{"ok": true}))   // {id}/rules, {id}/hide
	mux.HandleFunc("PUT /v1/workstreams/", devEcho(map[string]any{"ok": true})) // {key}/off
	mux.HandleFunc("POST /v1/config", devEcho(map[string]any{"host": "https://atlas-dev.keld.co", "restart_required": true}))

	return mux
}

func devEcho(body map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var discard map[string]any
		_ = json.NewDecoder(r.Body).Decode(&discard) // accept, never persist
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	}
}
