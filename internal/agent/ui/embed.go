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
	"strings"
	"sync/atomic"

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
// Two flows below are enough of an exception to earn their own simulation,
// because the page's Settings pane has real behaviour with nothing else to
// exercise it against until D2/D4's routes are mounted on the real daemon
// (see DevServer's own callers in cmd/ui-dev and this package's report):
//
//   - PUT /v1/settings answers `restart_required` FOR REAL (send_to_atlas or
//     dev_blocks in the patch), so the page's restart bar has something
//     honest to react to, and refuses a dev_blocks change exactly the way
//     docs/v3/contracts.md documents (409 turn_off_send_to_atlas_first) when
//     the loaded settings.json fixture has send_to_atlas on.
//   - PUT /v1/settings?restart=1 arms a short countdown that makes the next
//     two GET /v1/ledger calls answer 503, then resume — the
//     "restart_required, then 503 twice, then 200" flow the D2 brief asks
//     for, driven from any fixtures directory (fixtures/restart-required is
//     just the obviously-named place to point at for it).
func DevServer(fixturesDir string) http.Handler {
	mux := http.NewServeMux()
	// Same cookie-setting wrapper Route() uses in production, so
	// http://127.0.0.1:8899/?secret=anything exercises the real "take the
	// secret once, remember it as a cookie" flow locally too — DevServer
	// itself checks no secret at all, but the page's own behavior should
	// look identical whichever server is behind it.
	mux.Handle("/", withSecretCookie(assetHandler()))

	readFixture := func(filename string) ([]byte, bool) {
		b, err := os.ReadFile(filepath.Join(fixturesDir, filename))
		return b, err == nil
	}
	serveFixture := func(filename string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			b, ok := readFixture(filename)
			if !ok {
				http.Error(w, `{"error":"daemon_not_running"}`, http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
		}
	}

	// restartCountdown: how many more GET /v1/ledger calls should still
	// answer 503 after a `?restart=1` PUT — the "daemon is bouncing" window
	// the page's restart bar polls through. Package-level state is fine here:
	// DevServer is a single-operator manual review tool, never a concurrent
	// test fixture.
	var restartCountdown int32

	mux.HandleFunc("GET /v1/ledger", func(w http.ResponseWriter, r *http.Request) {
		if atomic.LoadInt32(&restartCountdown) > 0 {
			atomic.AddInt32(&restartCountdown, -1)
			http.Error(w, `{"error":"daemon_restarting"}`, http.StatusServiceUnavailable)
			return
		}
		serveFixture("ledger.json")(w, r)
	})
	mux.HandleFunc("GET /v1/settings", serveFixture("settings.json"))
	mux.HandleFunc("GET /v1/projects", serveFixture("projects.json"))

	mux.HandleFunc("PUT /v1/settings", func(w http.ResponseWriter, r *http.Request) {
		var patch map[string]any
		_ = json.NewDecoder(r.Body).Decode(&patch)

		if devBlocks, asked := patch["dev_blocks"]; asked {
			if s, ok := devBlocks.(string); ok && s != "" && fixtureAtlasOn(fixturesDir) {
				writeDevJSON(w, http.StatusConflict, map[string]any{"error": "turn_off_send_to_atlas_first"})
				return
			}
		}

		_, sendToAtlasAsked := patch["send_to_atlas"]
		_, devBlocksAsked := patch["dev_blocks"]
		restartRequired := sendToAtlasAsked || devBlocksAsked

		if r.URL.Query().Get("restart") == "1" {
			atomic.StoreInt32(&restartCountdown, 2) // next 2 GET /v1/ledger calls 503, then resume
		}
		writeDevJSON(w, http.StatusOK, map[string]any{"restart_required": restartRequired})
	})
	mux.HandleFunc("POST /v1/projects/bundle", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Title string `json:"title"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		writeDevJSON(w, http.StatusOK, localOnlyEcho(map[string]any{
			"project": map[string]any{"id": "p_dev_" + strings.ToLower(strings.ReplaceAll(body.Title, " ", "_")), "title": body.Title},
		}))
	})
	// POST /v1/service/restart answers 202 and nothing else, which is the
	// whole contract: "accepted", never "fixed". DevServer deliberately does
	// NOT then flip its ledger.json to a healthy `service` block — a fixture
	// that healed itself on the button press would demonstrate the exact lie
	// this control is built not to tell. Point --fixtures at
	// fixtures/service-stuck and the banner correctly sits on "Restart
	// requested" while the ledger keeps saying stuck.
	mux.HandleFunc("POST /v1/service/restart", func(w http.ResponseWriter, r *http.Request) {
		var discard map[string]any
		_ = json.NewDecoder(r.Body).Decode(&discard)
		writeDevJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
	})
	mux.HandleFunc("POST /v1/projects/place", devEcho(localOnlyEcho(nil)))
	mux.HandleFunc("POST /v1/projects/", devEcho(localOnlyEcho(nil)))   // {id}/rules, {id}/hide
	mux.HandleFunc("PUT /v1/workstreams/", devEcho(localOnlyEcho(nil))) // {key}/off

	mux.HandleFunc("POST /v1/config", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code string `json:"code"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		code := strings.TrimSpace(body.Code)
		switch {
		case code == "" || !strings.Contains(code, "/"):
			// No "host/CODE" shape at all — the 400 docs/v3/contracts.md
			// documents for a malformed code.
			writeDevJSON(w, http.StatusBadRequest, map[string]any{"error": "malformed_code"})
		case strings.Contains(strings.ToLower(code), "atlas-off"):
			// A code containing "atlas-off" is this dev server's own trigger
			// for the 409 docs/v3/contracts.md documents for POST /v1/config
			// while send_to_atlas is false — there is no real settings state
			// here to check against, so a manual reviewer asks for it by name.
			writeDevJSON(w, http.StatusConflict, map[string]any{"error": "atlas_off"})
		default:
			b, ok := readFixture("config.json")
			if !ok {
				http.Error(w, `{"error":"daemon_not_running"}`, http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(b)
		}
	})

	return mux
}

// fixtureAtlasOn reads the fixture directory's OWN settings.json for
// send_to_atlas, to decide the dev_blocks 409 simulation — DevServer never
// persists a PUT (see this function's caller), so the question is always
// "what does the loaded fixture say", matching the "fixture reviewer, not a
// second implementation" scope this file states above. Absent/unreadable
// defaults to true (Settings.AtlasEnabled()'s own default), matching
// app.js's atlasEnabled().
func fixtureAtlasOn(fixturesDir string) bool {
	b, err := os.ReadFile(filepath.Join(fixturesDir, "settings.json"))
	if err != nil {
		return true
	}
	var v struct {
		SendToAtlas *bool `json:"send_to_atlas"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		return true
	}
	if v.SendToAtlas == nil {
		return true
	}
	return *v.SendToAtlas
}

// localOnlyEcho adds the local_only/atlas_editor_url pair every real
// mutating /v1/projects (or /v1/workstreams) route stamps on
// (docs/v3/contracts.md's verified note), so the page's confirmation UI has
// something to react to against the dev server too, not only a live daemon.
func localOnlyEcho(v map[string]any) map[string]any {
	if v == nil {
		v = map[string]any{}
	}
	v["local_only"] = true
	v["atlas_editor_url"] = "https://atlas-dev.keld.co/workstreams"
	return v
}

func writeDevJSON(w http.ResponseWriter, status int, body map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func devEcho(body map[string]any) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var discard map[string]any
		_ = json.NewDecoder(r.Body).Decode(&discard) // accept, never persist
		writeDevJSON(w, http.StatusOK, body)
	}
}
