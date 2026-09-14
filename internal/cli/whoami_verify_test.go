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

func TestWhoamiAcceptsVerifyAndJSONFlags(t *testing.T) {
	cmd := newWhoamiCmd()
	for _, name := range []string{"verify", "json"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Fatalf("keld whoami must accept --%s: the installer pane reads one NDJSON "+
				"line to decide whether this machine is already connected", name)
		}
	}
}
