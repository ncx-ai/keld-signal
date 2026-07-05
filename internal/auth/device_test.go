package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// deviceServer returns an httptest server that completes the device flow on the
// first poll, handing back the given token/principal/org, and records poll hits.
func deviceServer(t *testing.T, token, principal, org string, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cli/device/start":
			w.Write([]byte(`{"device_code":"dc","user_code":"UC","verification_url":"https://v","interval":1,"expires_in":10}`))
		case "/v1/cli/device/poll":
			*hits++
			w.Write([]byte(`{"access_token":"` + token + `","principal":"` + principal + `","org":"` + org + `"}`))
		}
	}))
	return srv
}

// force=true makes `keld login` re-authenticate even when (possibly stale) creds
// are already stored — it must NOT short-circuit on them, and it must persist the
// fresh token. Regression test for stored creds surviving a server-side token
// reset (e.g. a DB reseed) and being silently trusted.
func TestRequireAuthForceReauthenticatesIgnoringStoredCreds(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := Save(AuthData{AccessToken: "STALE", Principal: "old", Org: "old", APIURL: "http://old"}); err != nil {
		t.Fatal(err)
	}
	hits := 0
	srv := deviceServer(t, "FRESH", "admin@acme.test", "Acme", &hits)
	defer srv.Close()
	paths.SetAPIBaseOverride(srv.URL)
	defer paths.SetAPIBaseOverride("")

	got, err := RequireAuth(false, false, true) // force, no browser
	if err != nil {
		t.Fatalf("force login: %v", err)
	}
	if hits == 0 {
		t.Fatal("force login short-circuited on stored creds — device flow never ran")
	}
	if got.AccessToken != "FRESH" {
		t.Fatalf("expected FRESH token, got %q", got.AccessToken)
	}
	if reloaded, _ := Load(); reloaded == nil || reloaded.AccessToken != "FRESH" {
		t.Fatalf("fresh creds not persisted: %v", reloaded)
	}
}

// force=false stays lazy: stored creds are returned without a network round-trip.
func TestRequireAuthLazyReturnsStoredCreds(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := Save(AuthData{AccessToken: "STORED", Principal: "p", Org: "o", APIURL: "http://x"}); err != nil {
		t.Fatal(err)
	}
	hits := 0
	srv := deviceServer(t, "FRESH", "p", "o", &hits)
	defer srv.Close()
	paths.SetAPIBaseOverride(srv.URL)
	defer paths.SetAPIBaseOverride("")

	got, err := RequireAuth(false, false, false) // lazy
	if err != nil {
		t.Fatalf("lazy auth: %v", err)
	}
	if hits != 0 {
		t.Fatal("lazy path hit the server — it should return stored creds directly")
	}
	if got.AccessToken != "STORED" {
		t.Fatalf("expected STORED token, got %q", got.AccessToken)
	}
}

func TestLoginPollsThenSucceeds(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cli/device/start":
			w.Write([]byte(`{"device_code":"dc","user_code":"UC","verification_url":"https://v","interval":1,"expires_in":10}`))
		case "/v1/cli/device/poll":
			calls++
			if calls < 2 {
				w.WriteHeader(202)
				return
			}
			w.Write([]byte(`{"access_token":"AT","principal":"p","org":"o"}`))
		}
	}))
	defer srv.Close()
	got, err := Login(api.NewClient(srv.URL, ""), false, func(time.Duration) {}, func(string) error { return nil })
	if err != nil || got.AccessToken != "AT" {
		t.Fatalf("login %v %v", got, err)
	}
}

func TestLoginContinuesWhenBrowserOpenFails(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cli/device/start":
			w.Write([]byte(`{"device_code":"dc","user_code":"UC","verification_url":"https://v","interval":1,"expires_in":10}`))
		case "/v1/cli/device/poll":
			w.Write([]byte(`{"access_token":"AT","principal":"p","org":"o"}`))
		}
	}))
	defer srv.Close()
	// openBrowser=true with an opener that always fails (e.g. headless/SSH/CI).
	// Login must still proceed to poll and succeed.
	got, err := Login(api.NewClient(srv.URL, ""), true, func(time.Duration) {}, func(string) error {
		return errors.New("no browser available")
	})
	if err != nil {
		t.Fatalf("login should not abort on browser-open failure: %v", err)
	}
	if got == nil || got.AccessToken != "AT" {
		t.Fatalf("expected AuthData with AT, got %v", got)
	}
}

func TestLoginTimesOut(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cli/device/start":
			// expires_in=1, interval=1 → loop runs once then times out
			w.Write([]byte(`{"device_code":"dc","user_code":"UC","verification_url":"https://v","interval":1,"expires_in":1}`))
		case "/v1/cli/device/poll":
			w.WriteHeader(202)
		}
	}))
	defer srv.Close()
	_, err := Login(api.NewClient(srv.URL, ""), false, func(time.Duration) {}, func(string) error { return nil })
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if err.Error() != "login timed out; please run `keld login` again" {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// TestLoginPollsDespiteBlockingOpener is the regression test for the hang where
// the browser opener blocked the device-poll loop (some Linux xdg-open setups do
// not return until the browser closes). Login must poll and succeed even when the
// opener never returns.
func TestLoginPollsDespiteBlockingOpener(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/cli/device/start":
			w.Write([]byte(`{"device_code":"dc","user_code":"UC","verification_url":"https://v","interval":1,"expires_in":10}`))
		case "/v1/cli/device/poll":
			w.Write([]byte(`{"access_token":"AT","principal":"p","org":"o"}`))
		}
	}))
	defer srv.Close()

	block := make(chan struct{})
	defer close(block)
	blockingOpener := func(string) error { <-block; return nil } // never returns until the test ends

	done := make(chan *AuthData, 1)
	go func() {
		got, err := Login(api.NewClient(srv.URL, ""), true, func(time.Duration) {}, blockingOpener)
		if err == nil {
			done <- got
		}
	}()

	select {
	case got := <-done:
		if got == nil || got.AccessToken != "AT" {
			t.Fatalf("expected AuthData with AT, got %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Login blocked on the browser opener — the device-poll loop never ran")
	}
}

// Stored creds carry the server the token belongs to (APIURL). Subsequent
// commands must target that server, not the built-in default — otherwise a
// local token is sent to atlas.keld.co and 401s. Regression for the
// `keld signal setup` 401 after a local login.
func TestRequireAuthTargetsStoredAPIURL(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	paths.SetAPIBaseOverride("") // no --api-url flag active
	defer paths.SetAPIBaseOverride("")
	if err := Save(AuthData{AccessToken: "T", Principal: "p", Org: "o", APIURL: "http://localhost:8000"}); err != nil {
		t.Fatal(err)
	}
	got, err := RequireAuth(false, false, false) // lazy
	if err != nil {
		t.Fatal(err)
	}
	if got.APIURL != "http://localhost:8000" {
		t.Fatalf("APIURL = %q", got.APIURL)
	}
	if paths.APIBase() != "http://localhost:8000" {
		t.Fatalf("APIBase should follow the stored token's server, got %q", paths.APIBase())
	}
}

// An explicit --api-url override (set before RequireAuth) beats the stored URL.
func TestRequireAuthFlagOverrideBeatsStoredAPIURL(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	paths.SetAPIBaseOverride("http://flag:9000")
	defer paths.SetAPIBaseOverride("")
	if err := Save(AuthData{AccessToken: "T", Principal: "p", Org: "o", APIURL: "http://localhost:8000"}); err != nil {
		t.Fatal(err)
	}
	if _, err := RequireAuth(false, false, false); err != nil {
		t.Fatal(err)
	}
	if paths.APIBase() != "http://flag:9000" {
		t.Fatalf("explicit flag override should win, got %q", paths.APIBase())
	}
}
