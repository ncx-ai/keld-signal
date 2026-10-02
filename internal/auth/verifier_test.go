package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/api"
)

func TestLoginWithCodeVerifierSendsItAndPersists(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("content-type", "application/json")
		w.Write([]byte(`{"access_token":"tok","principal":"p@x","org":"Acme"}`))
	}))
	defer srv.Close()

	a, err := LoginWithCodeVerifier(api.NewClient(srv.URL, ""), "AB12-CD34", "ver")
	if err != nil {
		t.Fatal(err)
	}
	if got["code_verifier"] != "ver" {
		t.Fatalf("verifier not sent: %v", got)
	}
	stored, _ := Load()
	if stored == nil || stored.AccessToken != "tok" || a.APIURL != srv.URL {
		t.Fatalf("auth.json not persisted: %+v", stored)
	}
}
