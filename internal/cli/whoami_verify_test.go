package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/auth"
)

// ⚠️ `keld whoami` has never contacted Atlas — it reads auth.json and prints the
// fields, so a revoked token and a live one look identical to it. The macOS
// installer's wizard pane needs the difference: it enables Continue only for a
// machine that is genuinely connected, and an "already paired" claim resting on
// a file's existence would let someone install with a dead credential and
// collect nothing. These tests pin the three outcomes apart.

func TestVerifyIdentityReportsVerifiedWhenAtlasAcceptsTheToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer live-token" {
			t.Errorf("Authorization = %q, want the stored token", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"endpoint":"https://atlas.example/v1","ingest_token":"ing","actor":"a"}`))
	}))
	defer srv.Close()

	got := verifyIdentity(&auth.AuthData{
		AccessToken: "live-token", Principal: "dg@keld.co", Org: "acme", APIURL: srv.URL,
	})
	if got.Status != identityVerified {
		t.Fatalf("status = %q, want %q (message: %s)", got.Status, identityVerified, got.Message)
	}
	if got.Principal != "dg@keld.co" || got.Org != "acme" {
		t.Fatalf("identity = %q/%q, want dg@keld.co/acme", got.Principal, got.Org)
	}
}

func TestVerifyIdentityReportsUnauthorizedOnARevokedToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"token revoked"}`))
	}))
	defer srv.Close()

	got := verifyIdentity(&auth.AuthData{AccessToken: "dead", APIURL: srv.URL})
	if got.Status != identityUnauthorized {
		t.Fatalf("status = %q, want %q — a 401 must never read as connected", got.Status, identityUnauthorized)
	}
}

func TestVerifyIdentityReportsUnreachableWhenAtlasCannotBeContacted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close() // nothing is listening now

	got := verifyIdentity(&auth.AuthData{AccessToken: "live-token", APIURL: url})
	if got.Status != identityUnreachable {
		t.Fatalf("status = %q, want %q — a transport failure is not a rejection", got.Status, identityUnreachable)
	}
}

func TestVerifyIdentityReportsUnreachableOnAServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	got := verifyIdentity(&auth.AuthData{AccessToken: "live-token", APIURL: srv.URL})
	if got.Status != identityUnreachable {
		t.Fatalf("status = %q, want %q — a 5xx says nothing about the credential", got.Status, identityUnreachable)
	}
}

func TestVerifyIdentityReportsNoneWithoutStoredCredentials(t *testing.T) {
	got := verifyIdentity(nil)
	if got.Status != identityNone {
		t.Fatalf("status = %q, want %q", got.Status, identityNone)
	}
}

func TestIdentityEventCarriesStatusOnTheWire(t *testing.T) {
	b, err := json.Marshal(identityEvent{
		Event: "identity", Status: identityVerified,
		Principal: "dg@keld.co", Org: "acme", APIURL: "https://atlas.keld.co",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["event"] != "identity" || got["status"] != string(identityVerified) {
		t.Fatalf("wire shape = %v; the installer pane keys on event+status", got)
	}
}

// The macOS installer's wizard pane starts the device flow itself and renders
// the returned code beside the verification URL — matching that code against the
// browser is the flow's anti-phishing step. It reads both by name off this
// event, so renaming either field silently turns the pane's sign-in screen into
// a spinner with no code on it.
func TestDeviceCodeEventCarriesTheFieldsTheInstallerRenders(t *testing.T) {
	b, err := json.Marshal(deviceCodeEvent{
		Event: "device_code", VerificationURL: "https://atlas.keld.co/device",
		UserCode: "WXYZ-1234", ExpiresIn: 900, Interval: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"event", "verification_url", "user_code"} {
		if _, ok := got[key]; !ok {
			t.Fatalf("device_code event is missing %q; the installer pane reads it by name (got %v)", key, got)
		}
	}
}

// ⚠️ The pane embeds the approval page in a ~560x300 web view, and
// `verification_url` points at a page built for a browser window — it redirects
// to the full login when unauthenticated and lays itself out with min-h-screen.
// Atlas therefore returns a second URL for the compact route, and the pane must
// be TOLD it rather than deriving it by patching Atlas's path: an installer
// already on people's machines cannot be corrected when a route moves.
func TestDeviceCodeEventCarriesTheInstallerURL(t *testing.T) {
	b, err := json.Marshal(deviceCodeEvent{
		Event: "device_code", VerificationURL: "https://atlas.keld.co/cli/signal?code=WXYZ-1234",
		InstallerURL: "https://atlas.keld.co/cli/installer?code=WXYZ-1234", UserCode: "WXYZ-1234",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got["installer_url"] != "https://atlas.keld.co/cli/installer?code=WXYZ-1234" {
		t.Fatalf("device_code event must carry installer_url for the embedded pane; got %v", got)
	}
}

// An Atlas that predates the compact route sends no installer_url, and the pane
// must still work — falling back to the browser-shaped page rather than loading
// an empty URL.
func TestDeviceCodeEventOmitsTheInstallerURLWhenAtlasDoesNotOfferOne(t *testing.T) {
	b, err := json.Marshal(deviceCodeEvent{
		Event: "device_code", VerificationURL: "https://atlas.keld.co/cli/signal?code=WXYZ-1234",
		UserCode: "WXYZ-1234",
	})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if _, present := got["installer_url"]; present {
		t.Fatalf("installer_url must be omitted when absent, so the pane can tell it apart from an empty one; got %v", got)
	}
}

func TestWhoamiAcceptsVerifyAndJSONFlags(t *testing.T) {
	cmd := newWhoamiCmd()
	for _, name := range []string{"verify", "json"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("keld whoami must accept --%s: the installer pane reads one NDJSON "+
				"line to decide whether this machine is already connected", name)
		}
	}
}
