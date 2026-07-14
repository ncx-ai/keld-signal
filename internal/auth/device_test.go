package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/console"
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

// Login must hand the freshly started device code to the onStart callback (the
// seam the --json login mode uses to emit a device_code event) before polling.
func TestLoginInvokesOnStartWithDeviceCode(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	hits := 0
	srv := deviceServer(t, "AT", "p", "o", &hits)
	defer srv.Close()

	var seen *api.DeviceStart
	got, err := Login(
		api.NewClient(srv.URL, ""),
		false,
		func(time.Duration) {},
		func(string) error { return nil },
		func(ds *api.DeviceStart) { seen = ds },
	)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if seen == nil || seen.UserCode != "UC" || seen.VerificationURL != "https://v" {
		t.Fatalf("onStart got %+v, want UserCode=UC url=https://v", seen)
	}
	if got == nil || got.Principal != "p" || got.Org != "o" {
		t.Fatalf("auth result %+v, want principal=p org=o", got)
	}
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
	got, err := Login(api.NewClient(srv.URL, ""), false, func(time.Duration) {}, func(string) error { return nil }, nil)
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
	}, nil)
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
	_, err := Login(api.NewClient(srv.URL, ""), false, func(time.Duration) {}, func(string) error { return nil }, nil)
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
		got, err := Login(api.NewClient(srv.URL, ""), true, func(time.Duration) {}, blockingOpener, nil)
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

// LoginWithCode redeems a one-time setup code against a stub /v1/cli/enroll and
// persists the resulting credentials, mirroring the device-flow's auth.json shape.
func TestLoginWithCodeSuccess(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	var gotBody map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/cli/enroll" {
			t.Fatalf("path %s", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Write([]byte(`{"access_token":"AT","principal":"dg@keld.co","org":"Acme"}`))
	}))
	defer srv.Close()

	got, err := LoginWithCode(api.NewClient(srv.URL, ""), "AB12-CD34")
	if err != nil {
		t.Fatalf("LoginWithCode: %v", err)
	}
	if gotBody["code"] != "AB12-CD34" {
		t.Fatalf("code sent to server = %q", gotBody["code"])
	}
	if got.AccessToken != "AT" || got.Principal != "dg@keld.co" || got.Org != "Acme" || got.APIURL != srv.URL {
		t.Fatalf("unexpected AuthData: %+v", got)
	}
	reloaded, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if reloaded == nil || reloaded.AccessToken != "AT" || reloaded.APIURL != srv.URL {
		t.Fatalf("auth.json not persisted correctly: %+v", reloaded)
	}
}

// A 410 (expired code) from the enroll endpoint must surface as an error and
// must NOT write auth.json.
func TestLoginWithCodeExpired(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(410)
	}))
	defer srv.Close()

	if _, err := LoginWithCode(api.NewClient(srv.URL, ""), "expired"); err == nil {
		t.Fatal("expected error for expired code")
	}
	if reloaded, _ := Load(); reloaded != nil {
		t.Fatalf("auth.json should not be written on failure, got %+v", reloaded)
	}
}

// defaultDeviceReport prints the unified human device-code line: a single
// "approve in your browser" instruction with the URL, no separate sentence
// about the code being pre-filled (that's implied by "code pre-filled").
func TestDefaultDeviceReportHumanFormat(t *testing.T) {
	var buf bytes.Buffer
	old := console.Out
	console.Out = &buf
	defer func() { console.Out = old }()

	defaultDeviceReport(&api.DeviceStart{VerificationURL: "https://v", UserCode: "UC"})

	got := buf.String()
	if !strings.Contains(got, "Approve this device in your browser (code pre-filled):") {
		t.Fatalf("missing approve line: %q", got)
	}
	if !strings.Contains(got, "https://v") {
		t.Fatalf("missing verification URL: %q", got)
	}
	if strings.Contains(got, "already filled in") || strings.Contains(got, "To authorize this device") {
		t.Fatalf("stale device-report wording still present: %q", got)
	}
}

// Login's human "opening the browser" notice uses the unified 2-space-indented
// wording.
func TestLoginPrintsOpeningBrowserHumanLine(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	hits := 0
	srv := deviceServer(t, "AT", "p", "o", &hits)
	defer srv.Close()

	var buf bytes.Buffer
	old := console.Out
	console.Out = &buf
	defer func() { console.Out = old }()

	_, err := Login(api.NewClient(srv.URL, ""), true, func(time.Duration) {}, func(string) error { return nil }, nil)
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if !strings.Contains(buf.String(), "  Opening your browser…") {
		t.Fatalf("expected unified opening-browser line, got %q", buf.String())
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
