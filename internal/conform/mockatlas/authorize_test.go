package mockatlas

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/auth"
)

var noRedirect = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func pkcePair() (verifier, challenge string) {
	verifier = strings.Repeat("v", 43)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func authorizeURL(base, redirect, state, challenge, method string) string {
	return base + "/cli/signal/authorize?" + url.Values{
		"redirect_uri": {redirect}, "state": {state},
		"code_challenge": {challenge}, "code_challenge_method": {method},
	}.Encode()
}

// The mock's authorize route redirects straight back (no Continue click — that
// is the real page's job), carrying a pairing code bound to the challenge.
func TestAuthorizeRedirectsBackWithABoundCode(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	ts, _ := newTestServer(t, Options{})
	verifier, challenge := pkcePair()
	state := strings.Repeat("s", 43)

	res, err := noRedirect.Get(authorizeURL(ts.URL, "http://127.0.0.1:53211/auth/callback", state, challenge, "S256"))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want 302", res.StatusCode)
	}
	loc, err := url.Parse(res.Header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	if loc.Scheme+"://"+loc.Host+loc.Path != "http://127.0.0.1:53211/auth/callback" {
		t.Fatalf("Location = %q, want the redirect_uri", loc)
	}
	if loc.Query().Get("state") != state {
		t.Fatalf("state not carried back: %q", loc.Query().Get("state"))
	}
	host, code, err := auth.ParsePairingCode(loc.Query().Get("pairing_code"))
	if err != nil || host != ts.URL {
		t.Fatalf("pairing_code %q: host %q err %v, want host %q", loc.Query().Get("pairing_code"), host, err, ts.URL)
	}

	// Wrong verifier: 410, and the code is burned for the right one too.
	c := api.NewClient(ts.URL, "")
	if _, err := c.EnrollWithVerifier(code, strings.Repeat("w", 43)); err == nil {
		t.Fatal("a wrong verifier must be refused")
	}
	if _, err := c.EnrollWithVerifier(code, verifier); err == nil {
		t.Fatal("a code refused once is consumed (C2: claim first)")
	}
}

func TestEnrollEnforcesTheChallenge(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	ts, _ := newTestServer(t, Options{})
	verifier, challenge := pkcePair()

	mint := func() string {
		res, err := noRedirect.Get(authorizeURL(ts.URL, "http://127.0.0.1:1024/auth/callback", strings.Repeat("s", 43), challenge, "S256"))
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		loc, _ := url.Parse(res.Header.Get("Location"))
		_, code, _ := auth.ParsePairingCode(loc.Query().Get("pairing_code"))
		return code
	}

	gone := func(verifier string) {
		t.Helper()
		body := `{"code":"` + mint() + `"`
		if verifier != "" {
			body += `,"code_verifier":"` + verifier + `"`
		}
		res := do(t, "POST", ts.URL+"/v1/cli/enroll", body+"}", map[string]string{"content-type": "application/json"})
		if res.StatusCode != http.StatusGone {
			t.Fatalf("verifier %q: status %d, want 410", verifier, res.StatusCode)
		}
	}
	gone("")                      // missing verifier
	gone(strings.Repeat("w", 43)) // wrong verifier

	code := mint()
	c := api.NewClient(ts.URL, "")
	if _, err := c.EnrollWithVerifier(code, verifier); err != nil {
		t.Fatalf("the right verifier must redeem: %v", err)
	}
	if _, err := c.EnrollWithVerifier(code, verifier); err == nil {
		t.Fatal("a browser code is single use")
	}

	// A setup code (no challenge) behaves exactly as before, verifier or not.
	if _, err := c.Enroll("TEST"); err != nil {
		t.Fatalf("setup code: %v", err)
	}
	if _, err := c.EnrollWithVerifier("TEST", verifier); err != nil {
		t.Fatalf("setup code with a stray verifier: %v", err)
	}
}

// C1's return-address, state and challenge rules. Every refusal mints nothing.
func TestAuthorizeRefusesBadInput(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	ts, srv := newTestServer(t, Options{})
	_, challenge := pkcePair()
	state := strings.Repeat("s", 43)
	good := "http://127.0.0.1:53211/auth/callback"

	cases := []struct{ name, redirect, state, challenge, method string }{
		{"port 80", "http://127.0.0.1:80/auth/callback", state, challenge, "S256"},
		{"port 0", "http://127.0.0.1:0/auth/callback", state, challenge, "S256"},
		{"port 70000", "http://127.0.0.1:70000/auth/callback", state, challenge, "S256"},
		{"port 1023", "http://127.0.0.1:1023/auth/callback", state, challenge, "S256"},
		{"no port", "http://127.0.0.1/auth/callback", state, challenge, "S256"},
		{"foreign host", "https://evil.example/auth/callback", state, challenge, "S256"},
		{"localhost", "http://localhost:5000/auth/callback", state, challenge, "S256"},
		{"ipv6", "http://[::1]:5000/auth/callback", state, challenge, "S256"},
		{"prefix trick", "http://127.0.0.1.evil.example:5000/auth/callback", state, challenge, "S256"},
		{"userinfo", "http://127.0.0.1@evil.example:5000/auth/callback", state, challenge, "S256"},
		{"decimal ip", "http://2130706433:5000/auth/callback", state, challenge, "S256"},
		{"https", "https://127.0.0.1:5000/auth/callback", state, challenge, "S256"},
		{"dotdot", "http://127.0.0.1:5000/auth/callback/../v1/config", state, challenge, "S256"},
		{"case", "http://127.0.0.1:5000/Auth/Callback", state, challenge, "S256"},
		{"trailing slash", "http://127.0.0.1:5000/auth/callback/", state, challenge, "S256"},
		{"query", "http://127.0.0.1:5000/auth/callback?x=1", state, challenge, "S256"},
		{"fragment", "http://127.0.0.1:5000/auth/callback#frag", state, challenge, "S256"},
		{"leading space", " " + good, state, challenge, "S256"},
		{"newline", good + "\n", state, challenge, "S256"},
		{"tab inside", "http://127.0.0.1:5000/auth/\tcallback", state, challenge, "S256"},
		{"no state", good, "", challenge, "S256"},
		{"long state", good, strings.Repeat("s", 129), challenge, "S256"},
		{"bad state", good, strings.Repeat("s", 42) + "!", challenge, "S256"},
		{"no challenge", good, state, "", "S256"},
		{"short challenge", good, state, challenge[:42], "S256"},
		{"plain method", good, state, challenge, "plain"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res, err := noRedirect.Get(authorizeURL(ts.URL, c.redirect, c.state, c.challenge, c.method))
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != http.StatusBadRequest || res.Header.Get("Location") != "" {
				t.Fatalf("status %d Location %q, want 400 and no redirect", res.StatusCode, res.Header.Get("Location"))
			}
		})
	}
	srv.mu.Lock()
	n := len(srv.grants)
	srv.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d grants minted by refused requests, want 0", n)
	}
}
