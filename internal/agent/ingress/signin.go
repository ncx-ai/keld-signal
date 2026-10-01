package ingress

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/hook"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/retry"
)

// The browser sign-in (docs/superpowers/specs/2026-09-29-signal-web-signin-discovery.html,
// contract C5): the page asks this daemon to start a sign-in, the system browser
// carries a random STATE to Atlas and back, and Atlas returns a two-minute code
// bound to an S256 challenge. Redeeming it also needs the VERIFIER, which never
// leaves this process — it is held in memory here and nowhere else.
//
//   POST /v1/auth/start  (page secret)  mint state + verifier, open the browser
//   GET  /auth/callback  (NO secret)    the browser's return; the state is the credential
//   GET  /v1/auth/state  (page secret)  what the page polls
//
// ⚠️ **GET /auth/callback IS THE ONE ROUTE ON THIS LISTENER THAT CHANGES STATE
// WITHOUT THE PAGE SECRET**, approved as the spec's one new pattern (D2). The
// return arrives in the system browser, which has no page cookie, so there is no
// other way back in. It is kept exactly that narrow: it acts only on a state this
// daemon minted in the last ten minutes, burns that state on first sight, writes
// nothing and calls nothing on any refusal, and every answer is a FIXED page —
// nothing from the URL is ever echoed into it.

const (
	maxPendingSignIns = 4
	pendingSignInTTL  = 10 * time.Minute
)

// last_error values the page renders (C5). An unrecognised one never appears.
const (
	signInNotStartedHere = "not_started_here"
	signInExpired        = "expired"
	signInAtlasMismatch  = "atlas_mismatch"
	signInAtlasOff       = "atlas_off"
	signInAtlasError     = "atlas_error"
	// The sign-in worked at Atlas and could not be saved HERE (hook.json):
	// this computer's failure, so not "Atlas could not finish".
	signInSaveFailed = "save_failed"
)

type pendingSignIn struct {
	state    string
	verifier string
	apiBase  string
	expires  time.Time
}

// signInStore holds the sign-ins this daemon started and has not yet seen come
// back: at most four, ten minutes each, memory only, single use. Safe for
// concurrent use.
type signInStore struct {
	now  func() time.Time
	open func(string) error

	mu      sync.Mutex
	entries []pendingSignIn // oldest first
	lastErr string
}

func newSignInStore(now func() time.Time, open func(string) error) *signInStore {
	return &signInStore{now: now, open: open}
}

// signIns is shared by every handler this process builds, because the daemon
// swaps its handler once in place (daemon/onboarding.go) and a sign-in started
// under one must be finishable under the other.
var signIns = newSignInStore(time.Now, auth.OpenURL)

// SignInRoute mounts the browser sign-in's three routes. Mounted in both the
// pre-config onboarding handler and the full one, beside ConfigRoute.
func SignInRoute() Route { return signInRoute(signIns) }

func signInRoute(s *signInStore) Route {
	return Route(func(mux *http.ServeMux, requireSecret func(http.Handler) http.Handler) {
		mux.Handle("POST /v1/auth/start", requireSecret(http.HandlerFunc(s.handleStart)))
		mux.Handle("GET /v1/auth/state", requireSecret(http.HandlerFunc(s.handleState)))
		// Deliberately NOT wrapped: see the file comment.
		mux.HandleFunc("GET /auth/callback", s.handleCallback)
	})
}

// randomB64 is n crypto/rand bytes as unpadded base64url.
func randomB64(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// begin mints and records a new pending sign-in, evicting the oldest when four
// are already held. It clears the last refusal, so Try again starts clean.
func (s *signInStore) begin(apiBase string) (pendingSignIn, error) {
	state, err := randomB64(32)
	if err != nil {
		return pendingSignIn{}, err
	}
	verifier, err := randomB64(32)
	if err != nil {
		return pendingSignIn{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e := pendingSignIn{state: state, verifier: verifier, apiBase: apiBase, expires: s.now().Add(pendingSignInTTL)}
	if len(s.entries) >= maxPendingSignIns {
		s.entries = append(s.entries[:0:0], s.entries[len(s.entries)-maxPendingSignIns+1:]...)
	}
	s.entries = append(s.entries, e)
	s.lastErr = ""
	return e, nil
}

// take removes the entry for state, whatever happens next — a state is usable
// once. Every held entry is compared in constant time, and the loop does not
// stop at a match. expired reports a found entry that outlived its ten minutes.
func (s *signInStore) take(state string) (e pendingSignIn, found, expired bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i := range s.entries {
		if subtle.ConstantTimeCompare([]byte(s.entries[i].state), []byte(state)) == 1 {
			idx = i
		}
	}
	if idx < 0 || state == "" {
		return pendingSignIn{}, false, false
	}
	e = s.entries[idx]
	s.entries = append(s.entries[:idx:idx], s.entries[idx+1:]...)
	return e, true, !s.now().Before(e.expires)
}

// pending reports whether any sign-in is still inside its ten minutes.
func (s *signInStore) pending() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pendingLocked()
}

func (s *signInStore) pendingLocked() bool {
	now := s.now()
	for _, e := range s.entries {
		if now.Before(e.expires) {
			return true
		}
	}
	return false
}

// refuse records why a return was refused, for the page's poll. A return whose
// state this daemon never issued (or already used) does not overwrite anything
// while a real sign-in is still pending: otherwise any page able to make the
// browser open a loopback URL could cancel the sign-in a person is finishing.
func (s *signInStore) refuse(reason string, knownState bool) {
	log.Printf("keld-agent: sign-in return refused (%s)", reason)
	s.mu.Lock()
	defer s.mu.Unlock()
	if !knownState && s.pendingLocked() {
		return
	}
	s.lastErr = reason
}

func (s *signInStore) succeeded() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastErr = ""
}

func (s *signInStore) lastError() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// ---- POST /v1/auth/start ----

type signInStartResponse struct {
	AuthorizeURL string `json:"authorize_url"`
	Opened       bool   `json:"opened"`
}

func (s *signInStore) handleStart(w http.ResponseWriter, r *http.Request) {
	if !settings.Load().AtlasEnabled() {
		writeError(w, http.StatusConflict, "send_to_atlas_is_off")
		return
	}
	port, ok := listenerPort(r)
	if !ok {
		writeError(w, http.StatusInternalServerError, "no_listener_port")
		return
	}
	e, err := s.begin(strings.TrimRight(paths.APIBase(), "/"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "no_randomness")
		return
	}
	sum := sha256.Sum256([]byte(e.verifier))
	q := url.Values{
		"redirect_uri":          {"http://127.0.0.1:" + strconv.Itoa(port) + "/auth/callback"},
		"state":                 {e.state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(sum[:])},
		"code_challenge_method": {"S256"},
	}
	authorizeURL := paths.AtlasWebBase() + "/cli/signal/authorize?" + q.Encode()

	opened := false
	if os.Getenv("KELD_AUTH_NO_BROWSER") != "1" && s.open != nil {
		// Best effort: the page shows the link whatever happens here.
		opened = s.open(authorizeURL) == nil
	}
	writeJSON(w, http.StatusOK, signInStartResponse{AuthorizeURL: authorizeURL, Opened: opened})
}

// listenerPort is the port this request actually arrived on — the listener the
// browser's return will reach. Read from the connection rather than the Host
// header (which the client writes) or agent.json (which describes the process,
// not this connection).
func listenerPort(r *http.Request) (int, bool) {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok {
		return 0, false
	}
	tcp, ok := addr.(*net.TCPAddr)
	if !ok || tcp.Port == 0 {
		return 0, false
	}
	return tcp.Port, true
}

// ---- GET /v1/auth/state ----

type signInStateResponse struct {
	Paired    bool    `json:"paired"`
	Principal *string `json:"principal"`
	Org       *string `json:"org"`
	FirstRun  bool    `json:"first_run"`
	Pending   bool    `json:"pending"`
	LastError *string `json:"last_error"`
}

func (s *signInStore) handleState(w http.ResponseWriter, r *http.Request) {
	out := signInStateResponse{Paired: isPaired(), Pending: s.pending()}
	if out.Paired {
		if a, err := auth.Load(); err == nil && a != nil {
			out.Principal, out.Org = nonEmpty(a.Principal), nonEmpty(a.Org)
		}
	}
	// First open: nobody has chosen yet. Not paired, the Send to Atlas switch
	// never written, and KELD_ATLAS not set — the env var already chose.
	out.FirstRun = !out.Paired && settings.Load().SendToAtlas == nil &&
		strings.TrimSpace(os.Getenv(settings.AtlasEnv)) == ""
	out.LastError = nonEmpty(s.lastError())
	writeJSON(w, http.StatusOK, out)
}

// isPaired is the same test awaitConfig applies: hook.json (or its env
// overrides) carries both an endpoint and an ingest token.
func isPaired() bool {
	cfg, err := hook.LoadConfig()
	return err == nil && cfg != nil && cfg.Endpoint != "" && cfg.IngestToken != ""
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ---- GET /auth/callback ----

func (s *signInStore) handleCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	e, found, expired := s.take(q.Get("state"))
	switch {
	case !found:
		s.refuse(signInNotStartedHere, false)
		writeSignInPage(w, http.StatusBadRequest, pageNotStartedHere)
		return
	case expired:
		s.refuse(signInExpired, true)
		writeSignInPage(w, http.StatusGone, pageStateExpired)
		return
	}

	host, code, err := auth.ParsePairingCode(q.Get("pairing_code"))
	if err != nil {
		s.refuse(signInAtlasError, true)
		writeSignInPage(w, http.StatusBadRequest, pageAtlasError)
		return
	}
	if host == "" {
		host = e.apiBase // a bare code names no Atlas; it is redeemed where we started
	}
	if !sameAtlas(host, e.apiBase) {
		s.refuse(signInAtlasMismatch, true)
		writeSignInPage(w, http.StatusBadRequest, pageAtlasMismatch)
		return
	}
	if !settings.Load().AtlasEnabled() {
		s.refuse(signInAtlasOff, true)
		writeSignInPage(w, http.StatusConflict, pageAtlasOff)
		return
	}

	if _, err := pair(e.apiBase, code, e.verifier); err != nil {
		var pe *pairError
		errors.As(err, &pe) // pair() returns nothing else
		// Which step failed, and how — the refusal reason alone left a failed
		// sign-in undiagnosable. ⚠️ Never a secret: redacted() scrubs what
		// pair() handled, and the state and raw pairing code are added here.
		log.Printf("keld-agent: sign-in failed at %s: %s", pe.stage, pe.redacted(e.state, q.Get("pairing_code")))
		var se *retry.StatusError
		switch {
		case pe.stage == pairLogin && errors.As(err, &se) && se.Code == http.StatusGone:
			s.refuse(signInExpired, true)
			writeSignInPage(w, http.StatusGone, pageCodeExpired)
		case pe.stage == pairHookWrite:
			s.refuse(signInSaveFailed, true)
			writeSignInPage(w, http.StatusInternalServerError, pageSaveFailed)
		default:
			s.refuse(signInAtlasError, true)
			writeSignInPage(w, http.StatusBadGateway, pageAtlasError)
		}
		return
	}
	s.succeeded()
	writeSignInPage(w, http.StatusOK, pageSignedIn)
}

// sameAtlas compares two API bases by scheme, host and port (default ports
// made explicit) and path. It is exact otherwise: localhost and 127.0.0.1 are
// different names, and a pairing code naming either when the sign-in started
// against the other is refused.
func sameAtlas(a, b string) bool {
	na, oka := normBase(a)
	nb, okb := normBase(b)
	return oka && okb && na == nb
}

func normBase(s string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "http":
			port = "80"
		case "https":
			port = "443"
		default:
			return "", false
		}
	}
	return scheme + "://" + strings.ToLower(u.Hostname()) + ":" + port + strings.TrimRight(u.Path, "/"), true
}

// ---- the fixed pages ----

type signInPage struct{ title, body string }

var (
	pageSignedIn       = signInPage{"Signed in", "Signed in. Close this tab."}
	pageNotStartedHere = signInPage{"Not signed in", "This sign-in was not started here. Start again from Signal."}
	pageStateExpired   = signInPage{"Not signed in", "This sign-in expired. Start again from Signal."}
	pageCodeExpired    = signInPage{"Not signed in", "That code expired. Start again from Signal."}
	pageAtlasMismatch  = signInPage{"Not signed in", "This sign-in came back from a different Atlas than the one Signal asked. Nothing was changed. Start again from Signal."}
	pageAtlasOff       = signInPage{"Not signed in", "Send to Atlas is off, so Signal did not sign in. Turn it on in Signal's Settings and try again."}
	pageAtlasError     = signInPage{"Not signed in", "Signal could not finish signing in. Start again from Signal."}
	pageSaveFailed     = signInPage{"Not signed in", "Signal couldn't save the sign-in on this computer. Start again from Signal."}
)

// writeSignInPage writes one of the constant pages above. Its only inputs are
// constants — there is no parameter through which request data could reach it.
func writeSignInPage(w http.ResponseWriter, status int, p signInPage) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><head><meta charset="utf-8">` +
		`<meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<title>Keld Signal · ` + p.title + `</title>` +
		`<style>body{font:16px/1.5 system-ui,sans-serif;margin:0;display:flex;min-height:100vh;` +
		`align-items:center;justify-content:center;background:#fafaf9;color:#1c1917}` +
		`@media (prefers-color-scheme:dark){body{background:#1c1917;color:#fafaf9}}` +
		`main{max-width:28rem;padding:1rem}</style></head>` +
		`<body><main><h1>Keld Signal</h1><p>` + p.body + `</p></main></body></html>`))
}
