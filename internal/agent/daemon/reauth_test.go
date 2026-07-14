package daemon

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/clientevents"
	"github.com/ncx-ai/keld-signal/internal/agent/creds"
	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/retry"
)

// testEmitter returns an Emitter with the gate open (Enabled, SevInfo floor,
// SampleRate 1) so Emit calls are reliably captured by Drain in assertions.
func testEmitter() *clientevents.Emitter {
	e := clientevents.NewEmitter(clientevents.Corr{}, 16)
	e.SetGate(clientevents.Gate{Enabled: true, MinSeverity: clientevents.SevInfo, SampleRate: 1})
	return e
}

func drainCodes(e *clientevents.Emitter) []string {
	var codes []string
	for _, ev := range e.Drain() {
		codes = append(codes, ev.Code)
	}
	return codes
}

func containsCode(codes []string, code string) bool {
	for _, c := range codes {
		if c == code {
			return true
		}
	}
	return false
}

func TestReauthRefreshSuccessSwapsTokenAndEmits(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	tok := creds.NewToken("old-ingest-token")
	emitter := testEmitter()
	r := newReauther(tok, emitter)
	r.now = func() time.Time { return time.Unix(1000, 0) }

	var savedEndpoint, savedToken string
	r.loadAuth = func() (*auth.AuthData, error) {
		return &auth.AuthData{AccessToken: "cli-token", APIURL: "https://atlas.example"}, nil
	}
	r.onboard = func(apiURL, cliToken string) (*api.Onboarding, error) {
		if apiURL != "https://atlas.example" || cliToken != "cli-token" {
			t.Fatalf("onboard called with unexpected args %q %q", apiURL, cliToken)
		}
		return &api.Onboarding{Endpoint: "https://atlas.example/v1/ingest", IngestToken: "new-ingest-token", Actor: "dg@keld.co"}, nil
	}
	r.save = func(endpoint, token string) error {
		savedEndpoint, savedToken = endpoint, token
		return nil
	}

	if err := r.refresh(context.Background()); err != nil {
		t.Fatalf("refresh() error = %v", err)
	}

	if savedEndpoint != "https://atlas.example/v1/ingest" || savedToken != "new-ingest-token" {
		t.Fatalf("save called with (%q, %q)", savedEndpoint, savedToken)
	}
	if got := tok.Get(); got != "new-ingest-token" {
		t.Fatalf("tok.Get() = %q, want new-ingest-token", got)
	}
	codes := drainCodes(emitter)
	if !containsCode(codes, "auth.refreshed") {
		t.Fatalf("codes = %v, want auth.refreshed", codes)
	}
	if _, err := os.Stat(paths.ReauthMarkerPath()); !os.IsNotExist(err) {
		t.Fatalf("marker should be absent after success, stat err = %v", err)
	}
	if r.TerminalRequired() {
		t.Fatal("TerminalRequired() should be false after success")
	}
}

func TestReauthOnboarding401IsTerminal(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	tok := creds.NewToken("old-ingest-token")
	emitter := testEmitter()
	r := newReauther(tok, emitter)
	r.now = func() time.Time { return time.Unix(2000, 0) }
	r.loadAuth = func() (*auth.AuthData, error) {
		return &auth.AuthData{AccessToken: "cli-token", APIURL: "https://atlas.example"}, nil
	}
	r.onboard = func(apiURL, cliToken string) (*api.Onboarding, error) {
		return nil, &retry.StatusError{Code: 401}
	}
	saveCalled := false
	r.save = func(endpoint, token string) error {
		saveCalled = true
		return nil
	}

	if err := r.refresh(context.Background()); err == nil {
		t.Fatal("refresh() should return an error on Onboarding 401")
	}

	if saveCalled {
		t.Fatal("save should not be called on 401")
	}
	if got := tok.Get(); got != "old-ingest-token" {
		t.Fatalf("tok.Get() = %q, want unchanged old-ingest-token", got)
	}
	if !r.TerminalRequired() {
		t.Fatal("TerminalRequired() should be true after a 401")
	}
	data, err := os.ReadFile(paths.ReauthMarkerPath())
	if err != nil {
		t.Fatalf("marker file should exist: %v", err)
	}
	if !strings.Contains(string(data), "re-authentication required") {
		t.Fatalf("marker contents = %q, want it to mention re-authentication required", string(data))
	}
	codes := drainCodes(emitter)
	if containsCode(codes, "auth.refreshed") {
		t.Fatalf("codes = %v, should not contain auth.refreshed", codes)
	}
}

func TestReauthNoAuthJSONIsTerminal(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	tok := creds.NewToken("old-ingest-token")
	emitter := testEmitter()
	r := newReauther(tok, emitter)
	r.now = func() time.Time { return time.Unix(3000, 0) }
	r.loadAuth = func() (*auth.AuthData, error) {
		return nil, nil // auth.Load's documented behavior when auth.json is missing
	}
	onboardCalled := false
	r.onboard = func(apiURL, cliToken string) (*api.Onboarding, error) {
		onboardCalled = true
		return nil, nil
	}

	if err := r.refresh(context.Background()); err == nil {
		t.Fatal("refresh() should return an error when there is no stored CLI credential")
	}
	if onboardCalled {
		t.Fatal("onboard should not be called when there is no auth.json")
	}
	if !r.TerminalRequired() {
		t.Fatal("TerminalRequired() should be true when auth.json is missing")
	}
	if _, err := os.Stat(paths.ReauthMarkerPath()); err != nil {
		t.Fatalf("marker file should exist: %v", err)
	}
}

func TestReauthCooldownAndSingleFlight(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	tok := creds.NewToken("old-ingest-token")
	r := newReauther(tok, testEmitter())
	fixedNow := time.Unix(4000, 0)
	r.now = func() time.Time { return fixedNow }
	r.cooldown = time.Minute

	var onboardCalls int
	r.loadAuth = func() (*auth.AuthData, error) {
		return &auth.AuthData{AccessToken: "cli-token", APIURL: "https://atlas.example"}, nil
	}
	r.onboard = func(apiURL, cliToken string) (*api.Onboarding, error) {
		onboardCalls++
		return &api.Onboarding{Endpoint: "https://atlas.example/v1/ingest", IngestToken: "new-ingest-token"}, nil
	}
	r.save = func(endpoint, token string) error { return nil }

	for i := 0; i < 5; i++ {
		if err := r.refresh(context.Background()); err != nil {
			t.Fatalf("refresh() call %d error = %v", i, err)
		}
	}

	if onboardCalls != 1 {
		t.Fatalf("onboard called %d times within the cooldown window, want 1", onboardCalls)
	}
}

func TestReauthTransientErrorIsNotTerminalAndRetriesAfterCooldown(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	tok := creds.NewToken("old-ingest-token")
	r := newReauther(tok, testEmitter())
	clock := time.Unix(5000, 0)
	r.now = func() time.Time { return clock }
	r.cooldown = time.Minute

	var onboardCalls int
	r.loadAuth = func() (*auth.AuthData, error) {
		return &auth.AuthData{AccessToken: "cli-token", APIURL: "https://atlas.example"}, nil
	}
	r.onboard = func(apiURL, cliToken string) (*api.Onboarding, error) {
		onboardCalls++
		return nil, errors.New("network error contacting Atlas: connection refused")
	}
	r.save = func(endpoint, token string) error {
		t.Fatal("save should not be called on a transient onboard error")
		return nil
	}

	if err := r.refresh(context.Background()); err == nil {
		t.Fatal("refresh() should return the transient error")
	}
	if r.TerminalRequired() {
		t.Fatal("a transient error should not set the terminal state")
	}
	if _, err := os.Stat(paths.ReauthMarkerPath()); !os.IsNotExist(err) {
		t.Fatalf("marker should be absent after a transient error, stat err = %v", err)
	}

	// Still within cooldown: a second call should skip onboard entirely.
	if err := r.refresh(context.Background()); err != nil {
		t.Fatalf("refresh() within cooldown should be a no-op, got err = %v", err)
	}
	if onboardCalls != 1 {
		t.Fatalf("onboard called %d times within cooldown, want 1", onboardCalls)
	}

	// Advance past the cooldown: a later call retries.
	clock = clock.Add(2 * time.Minute)
	if err := r.refresh(context.Background()); err == nil {
		t.Fatal("refresh() should still return the transient error on retry")
	}
	if onboardCalls != 2 {
		t.Fatalf("onboard called %d times after cooldown elapsed, want 2", onboardCalls)
	}
}

// TestReautherDefaultOnboardSeamDetects401 exercises the real production
// onboard seam (api.NewClient(...).Onboarding()) wired up by newReauther,
// against a live httptest server returning 401 — proving the wiring (not
// just an injected stub) correctly surfaces a *retry.StatusError.
func TestReautherDefaultOnboardSeamDetects401(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	r := newReauther(creds.NewToken("old"), testEmitter())
	_, err := r.onboard(srv.URL, "cli-token")
	if err == nil {
		t.Fatal("want error from the default onboard seam on 401")
	}
	var se *retry.StatusError
	if !errors.As(err, &se) || se.Code != 401 {
		t.Fatalf("errors.As(*retry.StatusError) failed on err = %v", err)
	}
}
