package ingress

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/conform/mockatlas"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// ---- fixtures ----

// pkceAtlas is a fake Atlas for the browser sign-in: /v1/cli/enroll redeems
// exactly one code, bound to the challenge the daemon sent, and answers 410
// when the verifier does not hash to it (C2). Every request is counted, so a
// refusal can assert Atlas was never asked anything.
type pkceAtlas struct {
	*httptest.Server
	mu        sync.Mutex
	challenge string // the code's bound challenge; "" = not yet authorized
	code      string
	gone      bool // answer every enroll with 410
	// enrollFail / onboardFail, when non-zero, answer that route with this
	// status and a body ECHOING what the daemon sent — the worst case for a
	// log line built from the error.
	enrollFail  int
	onboardFail int
	requests    atomic.Int32
	verifier    string // what the last enroll carried
	hold        func() // when set, enroll calls it first: a slow Atlas, held open by a test
}

func newPKCEAtlas(t *testing.T) *pkceAtlas {
	t.Helper()
	a := &pkceAtlas{code: "WXYZ-2345"}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/cli/enroll", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Code         string `json:"code"`
			CodeVerifier string `json:"code_verifier"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		a.mu.Lock()
		hold := a.hold
		a.mu.Unlock()
		if hold != nil {
			hold()
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		a.verifier = body.CodeVerifier
		if a.enrollFail != 0 {
			w.WriteHeader(a.enrollFail)
			fmt.Fprintf(w, "bad code %s verifier %s", body.Code, body.CodeVerifier)
			return
		}
		sum := sha256.Sum256([]byte(body.CodeVerifier))
		if a.gone || body.Code != a.code || a.challenge == "" ||
			base64.RawURLEncoding.EncodeToString(sum[:]) != a.challenge {
			w.WriteHeader(http.StatusGone)
			return
		}
		a.challenge = "" // single use
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "tok-web", "principal": "ana@acme.test", "org": "Acme",
		})
	})
	mux.HandleFunc("/v1/cli/onboarding", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-web" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		a.mu.Lock()
		fail := a.onboardFail
		a.mu.Unlock()
		if fail != 0 {
			w.WriteHeader(fail)
			fmt.Fprintf(w, "no onboarding for %s", r.Header.Get("Authorization"))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"endpoint": "https://ingest.acme/v1", "ingest_token": "ingest-web", "actor": "ana",
		})
	})
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.requests.Add(1)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(a.Close)
	return a
}

// authorize plays Atlas's /api/cli/authorize: bind the code to the challenge
// the authorize URL carried, and hand back the pairing code (host/CODE).
func (a *pkceAtlas) authorize(challenge string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.challenge = challenge
	return strings.TrimPrefix(a.URL, "http://") + "/" + a.code
}

type signInHarness struct {
	t      *testing.T
	store  *signInStore
	srv    *httptest.Server
	atlas  *pkceAtlas
	now    time.Time
	opened []string
	mu     sync.Mutex
}

// newSignInHarness isolates KELD_HOME, points the API base at a fake Atlas and
// serves the three routes behind the real secret gate.
func newSignInHarness(t *testing.T) *signInHarness {
	t.Helper()
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_ATLAS", "")
	t.Setenv("KELD_AUTH_NO_BROWSER", "")
	paths.SetAPIBaseOverride("")
	h := &signInHarness{t: t, atlas: newPKCEAtlas(t), now: time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)}
	t.Setenv("KELD_API_URL", h.atlas.URL)
	t.Setenv("KELD_ATLAS_WEB_URL", "http://atlas-web.test")
	h.store = newSignInStore(func() time.Time {
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.now
	}, func(u string) error {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.opened = append(h.opened, u)
		return nil
	})
	h.srv = httptest.NewServer(DiscardHandler("s3cret", signInRoute(h.store)))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *signInHarness) advance(d time.Duration) {
	h.mu.Lock()
	h.now = h.now.Add(d)
	h.mu.Unlock()
}

type startOut struct {
	AuthorizeURL string `json:"authorize_url"`
	Opened       bool   `json:"opened"`
}

func (h *signInHarness) start() (startOut, url.Values) {
	h.t.Helper()
	res := doRequest(h.t, h.srv, http.MethodPost, "/v1/auth/start", "s3cret", nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(res.Body)
		h.t.Fatalf("start: want 200, got %d: %s", res.StatusCode, b)
	}
	var out startOut
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	u, err := url.Parse(out.AuthorizeURL)
	if err != nil {
		h.t.Fatal(err)
	}
	return out, u.Query()
}

func (h *signInHarness) callback(rawQuery string) (*http.Response, string) {
	h.t.Helper()
	res, err := http.Get(h.srv.URL + "/auth/callback?" + rawQuery)
	if err != nil {
		h.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res, string(b)
}

type stateOut struct {
	Paired    bool    `json:"paired"`
	Principal *string `json:"principal"`
	Org       *string `json:"org"`
	FirstRun  bool    `json:"first_run"`
	Pending   bool    `json:"pending"`
	LastError *string `json:"last_error"`
}

func (h *signInHarness) state() stateOut {
	h.t.Helper()
	res := doRequest(h.t, h.srv, http.MethodGet, "/v1/auth/state", "s3cret", nil)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		h.t.Fatalf("state: want 200, got %d", res.StatusCode)
	}
	var out stateOut
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
		h.t.Fatal(err)
	}
	return out
}

func q(pairingCode, state string) string {
	return url.Values{"pairing_code": {pairingCode}, "state": {state}}.Encode()
}

var b64url43 = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }

func assertFixedPageHeaders(t *testing.T, res *http.Response) {
	t.Helper()
	want := map[string]string{
		"Content-Type":            "text/html; charset=utf-8",
		"Cache-Control":           "no-store",
		"Referrer-Policy":         "no-referrer",
		"Content-Security-Policy": "default-src 'none'; style-src 'unsafe-inline'",
	}
	for k, v := range want {
		if got := res.Header.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
}

// ---- AC-1 ----

func TestSignInStart(t *testing.T) {
	h := newSignInHarness(t)

	out, v := h.start()
	u, _ := url.Parse(out.AuthorizeURL)
	if u.Scheme+"://"+u.Host != "http://atlas-web.test" || u.Path != "/cli/signal/authorize" {
		t.Fatalf("authorize_url = %q, want http://atlas-web.test/cli/signal/authorize?…", out.AuthorizeURL)
	}

	// The return address is this listener's own port, on 127.0.0.1.
	srvURL, _ := url.Parse(h.srv.URL)
	wantRedirect := "http://127.0.0.1:" + srvURL.Port() + "/auth/callback"
	if v.Get("redirect_uri") != wantRedirect {
		t.Fatalf("redirect_uri = %q, want %q", v.Get("redirect_uri"), wantRedirect)
	}
	if v.Get("code_challenge_method") != "S256" {
		t.Fatalf("code_challenge_method = %q", v.Get("code_challenge_method"))
	}
	state := v.Get("state")
	if !b64url43.MatchString(state) {
		t.Fatalf("state %q is not 43 chars of base64url (32 random bytes)", state)
	}
	if raw, err := base64.RawURLEncoding.DecodeString(state); err != nil || len(raw) != 32 {
		t.Fatalf("state does not decode to 32 bytes: %v", err)
	}

	// The challenge is S256 of the verifier the daemon kept — and the verifier
	// itself never appears in the URL.
	h.store.mu.Lock()
	if len(h.store.entries) != 1 {
		h.store.mu.Unlock()
		t.Fatalf("want one pending sign-in, got %d", len(h.store.entries))
	}
	e := h.store.entries[0]
	h.store.mu.Unlock()
	if !b64url43.MatchString(e.verifier) {
		t.Fatalf("verifier %q is not 43 chars of base64url", e.verifier)
	}
	sum := sha256.Sum256([]byte(e.verifier))
	if v.Get("code_challenge") != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatal("code_challenge is not base64url(sha256(verifier))")
	}
	if strings.Contains(out.AuthorizeURL, e.verifier) {
		t.Fatal("the verifier must never ride the browser URL")
	}
	if e.apiBase != h.atlas.URL {
		t.Fatalf("stored api base = %q, want %q", e.apiBase, h.atlas.URL)
	}
	if e.state != state {
		t.Fatal("stored state differs from the URL's")
	}

	// The browser was opened, with exactly that URL.
	if !out.Opened || len(h.opened) != 1 || h.opened[0] != out.AuthorizeURL {
		t.Fatalf("opened=%v calls=%v, want one call with the authorize URL", out.Opened, h.opened)
	}

	// Two starts make two independent states.
	_, v2 := h.start()
	if v2.Get("state") == state || v2.Get("code_challenge") == v.Get("code_challenge") {
		t.Fatal("a second start reused the state or the challenge")
	}
}

func TestSignInStartHonoursNoBrowser(t *testing.T) {
	h := newSignInHarness(t)
	t.Setenv("KELD_AUTH_NO_BROWSER", "1")
	out, _ := h.start()
	if out.Opened || len(h.opened) != 0 {
		t.Fatalf("KELD_AUTH_NO_BROWSER=1: opened=%v calls=%v, want no browser", out.Opened, h.opened)
	}
	if out.AuthorizeURL == "" {
		t.Fatal("the page still needs the link to show")
	}
}

func TestSignInStartOpenerFailureIsNotAnError(t *testing.T) {
	h := newSignInHarness(t)
	h.store.open = func(string) error { return fmt.Errorf("no display") }
	out, _ := h.start()
	if out.Opened {
		t.Fatal("opened must be false when the opener failed")
	}
}

func TestSignInStartRefusedWhileAtlasOff(t *testing.T) {
	h := newSignInHarness(t)
	if err := os.WriteFile(paths.AgentConfigPath(), []byte(`{"send_to_atlas":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res := doRequest(t, h.srv, http.MethodPost, "/v1/auth/start", "s3cret", nil)
	body := decodeBody(t, res)
	if res.StatusCode != http.StatusConflict || body["error"] != "send_to_atlas_is_off" {
		t.Fatalf("want 409 send_to_atlas_is_off, got %d %v", res.StatusCode, body)
	}
	if len(h.opened) != 0 || h.state().Pending {
		t.Fatal("a refused start must open nothing and hold nothing")
	}
}

func TestSignInRoutesRequireTheSecretExceptTheCallback(t *testing.T) {
	h := newSignInHarness(t)
	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/v1/auth/start"},
		{http.MethodGet, "/v1/auth/state"},
	} {
		res := doRequest(t, h.srv, c.method, c.path, "", nil)
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s with no secret: got %d, want 401", c.method, c.path, res.StatusCode)
		}
	}
	// The callback is the one route with no secret — the browser has no page
	// cookie. It answers (a refusal page), it does not 401.
	res, _ := h.callback(q("x/ABCD-EFGH", "nope"))
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusNotFound {
		t.Fatalf("callback must be reachable without the secret, got %d", res.StatusCode)
	}
}

// ---- AC-5 ----

func TestSignInCallback(t *testing.T) {
	h := newSignInHarness(t)
	_, v := h.start()
	if !h.state().Pending {
		t.Fatal("after start, state must report pending")
	}

	code := h.atlas.authorize(v.Get("code_challenge"))
	res, body := h.callback(q(code, v.Get("state")))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("callback: want 200, got %d: %s", res.StatusCode, body)
	}
	assertFixedPageHeaders(t, res)
	if !strings.Contains(body, "Signed in. Close this tab.") {
		t.Fatalf("success page body = %q", body)
	}

	// The verifier went to Atlas, and pair() wrote both files.
	h.atlas.mu.Lock()
	sent := h.atlas.verifier
	h.atlas.mu.Unlock()
	if sent == "" {
		t.Fatal("the enroll carried no code_verifier")
	}
	authData, err := os.ReadFile(paths.AuthPath())
	if err != nil || !strings.Contains(string(authData), "tok-web") {
		t.Fatalf("auth.json not written by pair(): %v %s", err, authData)
	}
	hook, err := os.ReadFile(paths.HookConfigPath())
	if err != nil || !strings.Contains(string(hook), "ingest-web") {
		t.Fatalf("hook.json not written by pair(): %v %s", err, hook)
	}

	st := h.state()
	if !st.Paired || st.Pending || st.LastError != nil || st.FirstRun {
		t.Fatalf("after a good return: %+v", st)
	}
	if st.Principal == nil || *st.Principal != "ana@acme.test" || st.Org == nil || *st.Org != "Acme" {
		t.Fatalf("principal/org = %v/%v", st.Principal, st.Org)
	}
}

// The page polls once a second while a sign-in is in flight. A return takes its
// pending entry at once and then spends Atlas round trips pairing; a poll in
// that window used to see "not pending, not paired, no error" and the page
// declared the sign-in abandoned while it was succeeding. Measured on the real
// local-Atlas suite: about one run in eight.
func TestStateStaysPendingWhileAReturnIsPairing(t *testing.T) {
	for _, fail := range []bool{false, true} {
		h := newSignInHarness(t)
		_, v := h.start()
		code := h.atlas.authorize(v.Get("code_challenge"))
		entered, release := make(chan struct{}), make(chan struct{})
		h.atlas.mu.Lock()
		h.atlas.hold = func() { close(entered); <-release }
		if fail {
			h.atlas.enrollFail = http.StatusBadGateway
		}
		h.atlas.mu.Unlock()
		done := make(chan struct{})
		go func() { defer close(done); _, _ = h.callback(q(code, v.Get("state"))) }()
		<-entered
		// A forged return (a state this daemon never issued) lands while the
		// real one is pairing: it must not record its refusal over the real one.
		forged, _ := h.callback(q(code, "a-state-signal-never-issued-aaaaaaaaaaaaaaaa"))
		mid := h.state()
		close(release) // before any assertion, so a failing check cannot leave the server hung
		<-done
		if forged.StatusCode != http.StatusBadRequest {
			t.Fatalf("fail=%v: the forged return must be refused, got %d", fail, forged.StatusCode)
		}
		if !mid.Pending || mid.Paired || mid.LastError != nil {
			t.Fatalf("fail=%v: mid-pairing the page must still see a pending sign-in and no error: %+v", fail, mid)
		}
		st := h.state()
		switch {
		case fail && (st.Pending || st.Paired || st.LastError == nil || *st.LastError != signInAtlasError):
			t.Fatalf("after a failed pairing the page must see the real error: %+v", st)
		case !fail && (st.Pending || !st.Paired):
			t.Fatalf("after pairing the page must see signed in: %+v", st)
		}
	}
}

// ---- AC-6 (and the 410 row of the return-route table) ----

func TestSignInCallbackRefuses(t *testing.T) {
	const script = "<script>alert(1)</script>"
	cases := []struct {
		name     string
		run      func(h *signInHarness) (*http.Response, string)
		status   int
		wantPage string
		lastErr  string
		// atlasCalled: only the 410 row may reach Atlas — it is Atlas's answer.
		atlasCalled bool
	}{
		{
			name: "unknown state",
			run: func(h *signInHarness) (*http.Response, string) {
				return h.callback(q(strings.TrimPrefix(h.atlas.URL, "http://")+"/WXYZ-2345", strings.Repeat("A", 43)))
			},
			status: http.StatusBadRequest, wantPage: "This sign-in was not started here", lastErr: "not_started_here",
		},
		{
			name: "used state (replay)",
			run: func(h *signInHarness) (*http.Response, string) {
				_, v := h.start()
				code := h.atlas.authorize(v.Get("code_challenge"))
				if res, _ := h.callback(q(code, v.Get("state"))); res.StatusCode != http.StatusOK {
					h.t.Fatalf("first return should pair, got %d", res.StatusCode)
				}
				// Undo what the good return wrote, so "nothing written" is measurable.
				os.Remove(paths.AuthPath())
				os.Remove(paths.HookConfigPath())
				h.atlas.requests.Store(0)
				return h.callback(q(code, v.Get("state")))
			},
			status: http.StatusBadRequest, wantPage: "This sign-in was not started here", lastErr: "not_started_here",
		},
		{
			name: "expired state",
			run: func(h *signInHarness) (*http.Response, string) {
				_, v := h.start()
				code := h.atlas.authorize(v.Get("code_challenge"))
				h.advance(10*time.Minute + time.Second)
				return h.callback(q(code, v.Get("state")))
			},
			status: http.StatusGone, wantPage: "This sign-in expired", lastErr: "expired",
		},
		{
			name: "code names a different Atlas",
			run: func(h *signInHarness) (*http.Response, string) {
				_, v := h.start()
				h.atlas.authorize(v.Get("code_challenge"))
				return h.callback(q("evil.example/WXYZ-2345", v.Get("state")))
			},
			status: http.StatusBadRequest, wantPage: "different Atlas", lastErr: "atlas_mismatch",
		},
		{
			name: "Send to Atlas off",
			run: func(h *signInHarness) (*http.Response, string) {
				_, v := h.start()
				code := h.atlas.authorize(v.Get("code_challenge"))
				if err := os.WriteFile(paths.AgentConfigPath(), []byte(`{"send_to_atlas":false}`), 0o600); err != nil {
					h.t.Fatal(err)
				}
				return h.callback(q(code, v.Get("state")))
			},
			status: http.StatusConflict, wantPage: "Send to Atlas is off", lastErr: "atlas_off",
		},
		{
			name: "Atlas answers 410",
			run: func(h *signInHarness) (*http.Response, string) {
				_, v := h.start()
				code := h.atlas.authorize(v.Get("code_challenge"))
				h.atlas.mu.Lock()
				h.atlas.gone = true
				h.atlas.mu.Unlock()
				return h.callback(q(code, v.Get("state")))
			},
			status: http.StatusGone, wantPage: "That code expired", lastErr: "expired", atlasCalled: true,
		},
		{
			name: "script in every param, unknown state",
			run: func(h *signInHarness) (*http.Response, string) {
				return h.callback(q(script, script) + "&x=" + url.QueryEscape(script))
			},
			status: http.StatusBadRequest, wantPage: "This sign-in was not started here", lastErr: "not_started_here",
		},
		{
			name: "script in the pairing code, live state",
			run: func(h *signInHarness) (*http.Response, string) {
				_, v := h.start()
				return h.callback(q(script, v.Get("state")))
			},
			status: http.StatusBadRequest, wantPage: "different Atlas", lastErr: "atlas_mismatch",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newSignInHarness(t)
			res, body := c.run(h)
			if res.StatusCode != c.status {
				t.Fatalf("status = %d, want %d; body %s", res.StatusCode, c.status, body)
			}
			assertFixedPageHeaders(t, res)
			if !strings.Contains(body, c.wantPage) {
				t.Fatalf("page does not say %q: %s", c.wantPage, body)
			}
			if strings.Contains(strings.ToLower(body), "<script") || strings.Contains(body, "alert(") ||
				strings.Contains(body, "evil.example") || strings.Contains(body, "WXYZ") {
				t.Fatalf("the page echoed something from the URL: %s", body)
			}
			if fileExists(paths.AuthPath()) || fileExists(paths.HookConfigPath()) {
				t.Fatal("a refused return wrote auth.json or hook.json")
			}
			if n := h.atlas.requests.Load(); c.atlasCalled != (n > 0) {
				t.Fatalf("Atlas requests = %d, want called=%v", n, c.atlasCalled)
			}
			st := h.state()
			if st.Paired || st.Pending {
				t.Fatalf("after a refusal: %+v", st)
			}
			if st.LastError == nil || *st.LastError != c.lastErr {
				t.Fatalf("last_error = %v, want %q", st.LastError, c.lastErr)
			}
		})
	}
}

// Row 5: a return naming another Atlas burns the state, so the right code
// arriving afterwards is refused as well.
func TestMismatchedReturnBurnsTheState(t *testing.T) {
	h := newSignInHarness(t)
	_, v := h.start()
	code := h.atlas.authorize(v.Get("code_challenge"))
	h.callback(q("evil.example/WXYZ-2345", v.Get("state")))
	if res, _ := h.callback(q(code, v.Get("state"))); res.StatusCode == http.StatusOK {
		t.Fatal("a mismatched return must burn the state")
	}
	if fileExists(paths.AuthPath()) || h.atlas.requests.Load() != 0 {
		t.Fatal("nothing may be written or asked of Atlas")
	}
}

// A forged return must not cancel a real sign-in the page is waiting on.
func TestForgedReturnDoesNotDisturbAPendingSignIn(t *testing.T) {
	h := newSignInHarness(t)
	_, v := h.start()
	h.callback(q("x.test/ABCD-EFGH", strings.Repeat("B", 43)))
	st := h.state()
	if !st.Pending || st.LastError != nil {
		t.Fatalf("a forged return disturbed the pending sign-in: %+v", st)
	}
	code := h.atlas.authorize(v.Get("code_challenge"))
	if res, body := h.callback(q(code, v.Get("state"))); res.StatusCode != http.StatusOK {
		t.Fatalf("the real return should still pair, got %d: %s", res.StatusCode, body)
	}
}

// A new start clears the last refusal, so Try again starts clean.
func TestStartClearsLastError(t *testing.T) {
	h := newSignInHarness(t)
	h.callback(q("x.test/ABCD-EFGH", strings.Repeat("C", 43)))
	if h.state().LastError == nil {
		t.Fatal("precondition: a refusal sets last_error")
	}
	h.start()
	if st := h.state(); st.LastError != nil || !st.Pending {
		t.Fatalf("after a new start: %+v", st)
	}
}

// ---- first_run (AC-12's decision table, Signal half) ----

func TestAuthState(t *testing.T) {
	cases := []struct {
		name     string
		config   string // agent-config.json; "" = absent
		atlasEnv string
		paired   bool
		want     bool
	}{
		{name: "fresh machine", want: true},
		{name: "empty agent-config", config: `{}`, want: true},
		{name: "chose local only", config: `{"send_to_atlas":false}`, want: false},
		{name: "set on by hand", config: `{"send_to_atlas":true}`, want: false},
		{name: "KELD_ATLAS=0", atlasEnv: "0", want: false},
		{name: "KELD_ATLAS=1", atlasEnv: "1", want: false},
		{name: "paired", paired: true, want: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newSignInHarness(t)
			t.Setenv("KELD_ATLAS", c.atlasEnv)
			if c.config != "" {
				if err := os.WriteFile(paths.AgentConfigPath(), []byte(c.config), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if c.paired {
				if err := os.WriteFile(paths.HookConfigPath(), []byte(`{"endpoint":"https://i/v1","ingest_token":"t"}`), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			st := h.state()
			if st.FirstRun != c.want {
				t.Fatalf("first_run = %v, want %v (%+v)", st.FirstRun, c.want, st)
			}
			if st.Paired != c.paired {
				t.Fatalf("paired = %v, want %v", st.Paired, c.paired)
			}
			if !c.paired && (st.Principal != nil || st.Org != nil) {
				t.Fatalf("unpaired must report null principal/org, got %v/%v", st.Principal, st.Org)
			}
			if st.Pending || st.LastError != nil {
				t.Fatalf("nothing started: %+v", st)
			}
		})
	}
}

// The wire shape the page depends on: every key present, nulls as null.
func TestAuthStateWireShape(t *testing.T) {
	h := newSignInHarness(t)
	res := doRequest(t, h.srv, http.MethodGet, "/v1/auth/state", "s3cret", nil)
	got := decodeBody(t, res)
	for _, k := range []string{"paired", "principal", "org", "first_run", "pending", "last_error"} {
		if _, ok := got[k]; !ok {
			t.Fatalf("key %q missing from %v", k, got)
		}
	}
	if got["principal"] != nil || got["org"] != nil || got["last_error"] != nil {
		t.Fatalf("unpaired nulls must be JSON null: %v", got)
	}
}

// ---- the pending store ----

func TestSignInStoreEvictsTheOldestAndIsSingleUse(t *testing.T) {
	now := time.Now()
	s := newSignInStore(func() time.Time { return now }, nil)
	// Literal numbers, so moving the cap fails here: four held, the fifth
	// start evicts the oldest.
	if maxPendingSignIns != 4 {
		t.Fatalf("the cap is %d; the spec says 4", maxPendingSignIns)
	}
	var states []string
	for i := 0; i < 5; i++ {
		e, err := s.begin("http://a")
		if err != nil {
			t.Fatal(err)
		}
		states = append(states, e.state)
	}
	if _, ok, _ := s.take(states[0]); ok {
		t.Fatal("the oldest pending sign-in should have been evicted")
	}
	for _, st := range states[1:] {
		if _, ok, expired := s.take(st); !ok || expired {
			t.Fatalf("state %q should be pending", st)
		}
		if _, ok, _ := s.take(st); ok {
			t.Fatalf("state %q was usable twice", st)
		}
	}
	if s.pending() {
		t.Fatal("everything was taken")
	}
}

func TestSignInStoreExpiry(t *testing.T) {
	now := time.Now()
	s := newSignInStore(func() time.Time { return now }, nil)
	e, _ := s.begin("http://a")
	if pendingSignInTTL != 10*time.Minute {
		t.Fatalf("the pending TTL is %v; the spec says 10 minutes", pendingSignInTTL)
	}
	// One second short of ten minutes it still works; see the row just after.
	probe, _ := s.begin("http://a")
	now = now.Add(10*time.Minute - time.Second)
	if _, ok, expired := s.take(probe.state); !ok || expired {
		t.Fatalf("at 10m-1s: ok=%v expired=%v, want found and not expired", ok, expired)
	}
	now = now.Add(time.Second + time.Nanosecond)
	if s.pending() {
		t.Fatal("an expired sign-in is not pending")
	}
	if _, ok, expired := s.take(e.state); !ok || !expired {
		t.Fatalf("take after expiry: ok=%v expired=%v, want found and expired", ok, expired)
	}
	if _, ok, _ := s.take(e.state); ok {
		t.Fatal("an expired entry is still burned on first sight")
	}
}

func TestSignInStoreConcurrentTakeWinsOnce(t *testing.T) {
	s := newSignInStore(time.Now, nil)
	e, _ := s.begin("http://a")
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, ok, _ := s.take(e.state); ok {
				wins.Add(1)
			}
			_, _ = s.begin("http://b")
			_ = s.pending()
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("a state was taken %d times, want exactly once", wins.Load())
	}
	s.mu.Lock()
	n := len(s.entries)
	s.mu.Unlock()
	if n > maxPendingSignIns {
		t.Fatalf("store holds %d entries, cap is %d", n, maxPendingSignIns)
	}
}

// The whole round trip in one process: the daemon's start route, the mock
// Atlas's authorize redirect, the browser following it back to /auth/callback,
// and pair() redeeming with the verifier the mock enforces (C2). This is the
// Go twin of the Playwright spec, with an http.Client for a browser.
func TestSignInRoundTripThroughMockAtlas(t *testing.T) {
	h := newSignInHarness(t)
	mock, err := mockatlas.New(mockatlas.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ms := httptest.NewServer(mock)
	defer ms.Close()
	t.Setenv("KELD_API_URL", ms.URL)
	t.Setenv("KELD_ATLAS_WEB_URL", ms.URL)

	out, _ := h.start()
	res, err := http.Get(out.AuthorizeURL) // follows the 302 back to the daemon
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !strings.Contains(string(body), "Signed in. Close this tab.") {
		t.Fatalf("round trip ended at %d: %s", res.StatusCode, body)
	}
	hook, err := os.ReadFile(paths.HookConfigPath())
	if err != nil || !strings.Contains(string(hook), mockatlas.DefaultIngestToken) {
		t.Fatalf("hook.json after the round trip: %v %s", err, hook)
	}
	if st := h.state(); !st.Paired || st.Principal == nil {
		t.Fatalf("state after the round trip: %+v", st)
	}
}

// A pairing this process wrote is ANNOUNCED, so the daemon's pairing watcher
// adopts it at once rather than on its next poll — otherwise the page says
// "Signed in" while the health strip still says "Atlas not paired". The hook
// runs after hook.json is on disk (the watcher re-reads it) and never on a
// refusal.
func TestPairAnnouncesTheNewPairing(t *testing.T) {
	h := newSignInHarness(t)
	var calls atomic.Int32
	var hookOnDisk atomic.Bool
	SetOnPaired(func() {
		calls.Add(1)
		hookOnDisk.Store(fileExists(paths.HookConfigPath()))
	})
	t.Cleanup(func() { SetOnPaired(nil) })

	// A refusal announces nothing.
	h.callback(q("x.test/ABCD-EFGH", strings.Repeat("C", 43)))
	if n := calls.Load(); n != 0 {
		t.Fatalf("a refused return announced a pairing (%d calls)", n)
	}

	_, v := h.start()
	code := h.atlas.authorize(v.Get("code_challenge"))
	if res, body := h.callback(q(code, v.Get("state"))); res.StatusCode != http.StatusOK {
		t.Fatalf("callback: %d %s", res.StatusCode, body)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("a good return announced the pairing %d times, want 1", n)
	}
	if !hookOnDisk.Load() {
		t.Fatal("the pairing was announced before hook.json was written")
	}
}

// A sign-in that fails inside pair() says WHERE in the log — which step
// failed and the error — because "refused (atlas_error)" alone left a failed
// sign-in undiagnosable. ⚠️ Never the code, the verifier, the state or a
// token, even when Atlas's error body echoes them back. And a hook.json that
// cannot be written is this computer's failure, not Atlas's, so it has its
// own last_error and page.
func TestSignInFailureLogsTheStageAndNoSecret(t *testing.T) {
	cases := []struct {
		name     string
		setup    func(h *signInHarness)
		stage    string
		lastErr  string
		wantPage string
		status   int
	}{
		{
			name:  "Atlas refuses the redeem",
			setup: func(h *signInHarness) { h.atlas.enrollFail = http.StatusInternalServerError },
			stage: "login", lastErr: "atlas_error", wantPage: "could not finish signing in", status: http.StatusBadGateway,
		},
		{
			name:  "the onboarding hand-off fails",
			setup: func(h *signInHarness) { h.atlas.onboardFail = http.StatusServiceUnavailable },
			stage: "onboarding", lastErr: "atlas_error", wantPage: "could not finish signing in", status: http.StatusBadGateway,
		},
		{
			name: "hook.json cannot be written",
			setup: func(h *signInHarness) {
				// A directory where the file should go: WriteFile fails, nothing else does.
				if err := os.MkdirAll(paths.HookConfigPath(), 0o700); err != nil {
					h.t.Fatal(err)
				}
			},
			stage: "hook_write", lastErr: "save_failed", wantPage: "couldn't save the sign-in on this computer", status: http.StatusInternalServerError,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf strings.Builder
			var mu sync.Mutex
			log.SetOutput(writerFunc(func(p []byte) (int, error) {
				mu.Lock()
				defer mu.Unlock()
				return buf.Write(p)
			}))
			t.Cleanup(func() { log.SetOutput(os.Stderr) })

			h := newSignInHarness(t)
			c.setup(h)
			_, v := h.start()
			code := h.atlas.authorize(v.Get("code_challenge"))
			res, body := h.callback(q(code, v.Get("state")))
			if res.StatusCode != c.status {
				t.Fatalf("status = %d, want %d: %s", res.StatusCode, c.status, body)
			}
			if !strings.Contains(body, c.wantPage) {
				t.Fatalf("page does not say %q: %s", c.wantPage, body)
			}
			if st := h.state(); st.LastError == nil || *st.LastError != c.lastErr {
				t.Fatalf("last_error = %v, want %q", st.LastError, c.lastErr)
			}

			mu.Lock()
			logged := buf.String()
			mu.Unlock()
			if !strings.Contains(logged, "keld-agent: sign-in failed at "+c.stage+": ") {
				t.Fatalf("log does not name the stage %q:\n%s", c.stage, logged)
			}
			h.atlas.mu.Lock()
			verifier := h.atlas.verifier
			h.atlas.mu.Unlock()
			bareCode := code[strings.LastIndex(code, "/")+1:]
			for name, secret := range map[string]string{
				"code": bareCode, "pairing code": code, "verifier": verifier, "state": v.Get("state"),
				"access token": "tok-web", "ingest token": "ingest-web",
			} {
				if secret != "" && strings.Contains(logged, secret) {
					t.Fatalf("the log carries the %s:\n%s", name, logged)
				}
			}
		})
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }
