package ingress

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// fakeAtlas serves the three endpoints POST /v1/config's flow calls:
// /v1/cli/enroll (redeem the code) and /v1/cli/onboarding (fetch the
// endpoint + ingest token). It is what "against an httptest fake Atlas" means
// for this route.
func fakeAtlas(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/cli/enroll", func(w http.ResponseWriter, r *http.Request) {
		var body struct{ Code string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body.Code != "ABCD-EFGH" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"access_token": "tok-123", "principal": "gabriel@keld.co", "org": "keld",
		})
	})
	mux.HandleFunc("/v1/cli/onboarding", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok-123" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"endpoint": "https://ingest.example/v1", "ingest_token": "ingest-abc", "actor": "gabriel",
		})
	})
	return httptest.NewServer(mux)
}

// codeFor builds a pairing code embedding the fake Atlas's own loopback
// address, exactly the shape auth.ParsePairingCode exists to parse
// ("host/CODE") — see internal/auth/code.go's isLoopbackHost.
func codeFor(srv *httptest.Server) string {
	host := strings.TrimPrefix(srv.URL, "http://")
	return host + "/ABCD-EFGH"
}

func TestConfigRouteRequiresSecret(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(DiscardHandler("s3cret", ConfigRoute()))
	defer srv.Close()
	res, err := http.Post(srv.URL+"/v1/config", "application/json", strings.NewReader(`{"code":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", res.StatusCode)
	}
}

func TestConfigRouteSuccessWritesAuthAndHook(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	atlas := fakeAtlas(t)
	defer atlas.Close()

	srv := httptest.NewServer(DiscardHandler("s3cret", ConfigRoute()))
	defer srv.Close()

	res := doRequest(t, srv, http.MethodPost, "/v1/config", "s3cret", map[string]string{"code": codeFor(atlas)})
	if res.StatusCode != http.StatusOK {
		body := decodeBody(t, res)
		t.Fatalf("want 200, got %d: %v", res.StatusCode, body)
	}
	out := decodeBody(t, res)
	if out["host"] != atlas.URL {
		t.Fatalf("host = %v, want %v", out["host"], atlas.URL)
	}
	if out["restart_required"] != true {
		t.Fatalf("restart_required = %v, want true", out["restart_required"])
	}

	a, err := auth.Load()
	if err != nil || a == nil {
		t.Fatalf("auth.json not written: %v", err)
	}
	if a.AccessToken != "tok-123" || a.Org != "keld" || a.Principal != "gabriel@keld.co" {
		t.Fatalf("unexpected auth data: %+v", a)
	}

	hookData, err := os.ReadFile(paths.HookConfigPath())
	if err != nil {
		t.Fatalf("hook.json not written: %v", err)
	}
	var hook struct {
		Endpoint    string `json:"endpoint"`
		IngestToken string `json:"ingest_token"`
	}
	if err := json.Unmarshal(hookData, &hook); err != nil {
		t.Fatal(err)
	}
	if hook.Endpoint != "https://ingest.example/v1" || hook.IngestToken != "ingest-abc" {
		t.Fatalf("unexpected hook data: %+v", hook)
	}
}

// T12: malformed code -> 400, and NEITHER auth.json NOR hook.json is touched.
func TestConfigRouteMalformedCodeTouchesNoFile(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(DiscardHandler("s3cret", ConfigRoute()))
	defer srv.Close()

	res := doRequest(t, srv, http.MethodPost, "/v1/config", "s3cret", map[string]string{"code": ""})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", res.StatusCode)
	}
	if _, err := os.Stat(paths.AuthPath()); err == nil {
		t.Fatal("a malformed code must not write auth.json")
	}
	if _, err := os.Stat(paths.HookConfigPath()); err == nil {
		t.Fatal("a malformed code must not write hook.json")
	}
}

func TestConfigRouteMalformedJSONBody(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(DiscardHandler("s3cret", ConfigRoute()))
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/config", strings.NewReader("{not json"))
	req.Header.Set("x-keld-agent-secret", "s3cret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", res.StatusCode)
	}
}

// 409 while Send to Atlas is off — pairing must not silently hand a
// local-only machine a fresh Atlas credential.
func TestConfigRouteRefusedWhileAtlasOff(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := os.WriteFile(paths.AgentConfigPath(), []byte(`{"send_to_atlas":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	atlas := fakeAtlas(t)
	defer atlas.Close()

	srv := httptest.NewServer(DiscardHandler("s3cret", ConfigRoute()))
	defer srv.Close()

	res := doRequest(t, srv, http.MethodPost, "/v1/config", "s3cret", map[string]string{"code": codeFor(atlas)})
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", res.StatusCode)
	}
	if _, err := os.Stat(paths.AuthPath()); err == nil {
		t.Fatal("a refused pairing must not write auth.json")
	}
}

func TestConfigRouteInvalidCodeFromAtlasIsReported(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	atlas := fakeAtlas(t)
	defer atlas.Close()

	srv := httptest.NewServer(DiscardHandler("s3cret", ConfigRoute()))
	defer srv.Close()

	host := strings.TrimPrefix(atlas.URL, "http://")
	res := doRequest(t, srv, http.MethodPost, "/v1/config", "s3cret", map[string]string{"code": host + "/WRONG-CODE"})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", res.StatusCode)
	}
	if _, err := os.Stat(paths.AuthPath()); err == nil {
		t.Fatal("a code Atlas rejects must not write auth.json")
	}
}
