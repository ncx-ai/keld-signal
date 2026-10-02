package ingress

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

type unpairH struct {
	h        *signInHarness
	restarts *atomic.Int32
}

// newUnpairH serves the Unpair route on an isolated KELD_HOME with a fake
// restart, so a test can see whether Signal would have restarted.
func newUnpairH(t *testing.T) *unpairH {
	t.Helper()
	t.Setenv("KELD_CTX_ENDPOINT", "")
	t.Setenv("KELD_CTX_TOKEN", "")
	u := &unpairH{h: newSignInHarness(t), restarts: &atomic.Int32{}}
	u.h.srv.Config.Handler = DiscardHandler("s3cret", unpairRoute(u.h.store, func() error { u.restarts.Add(1); return nil }))
	return u
}

func (u *unpairH) do(t *testing.T) (int, map[string]any) {
	t.Helper()
	res := doRequest(t, u.h.srv, http.MethodPost, "/v1/auth/unpair", "s3cret", nil)
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	return res.StatusCode, body
}

func writePairing(t *testing.T) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(paths.HookConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.HookConfigPath(), []byte(`{"endpoint":"https://ingest.acme/v1","ingest_token":"ingest-web"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.AuthPath(), []byte(`{"access_token":"tok-web","principal":"ana@acme.test","org":"Acme"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitRestarts(t *testing.T, r *atomic.Int32, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && r.Load() != want {
		time.Sleep(10 * time.Millisecond)
	}
	if got := r.Load(); got != want {
		t.Fatalf("restarts = %d, want %d", got, want)
	}
}

func TestUnpairRemovesThePairingAndRestarts(t *testing.T) {
	u := newUnpairH(t)
	writePairing(t)
	code, body := u.do(t)
	if code != http.StatusOK || body["unpaired"] != true || body["restarting"] != true {
		t.Fatalf("unpair: %d %v", code, body)
	}
	for _, p := range []string{paths.HookConfigPath(), paths.AuthPath()} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s still exists after unpair (err=%v)", p, err)
		}
	}
	waitRestarts(t, u.restarts, 1)
}

func TestUnpairOnAnUnpairedMachineChangesNothing(t *testing.T) {
	u := newUnpairH(t)
	code, body := u.do(t)
	if code != http.StatusOK || body["unpaired"] != false || body["restarting"] != false {
		t.Fatalf("unpair when not paired: %d %v", code, body)
	}
	time.Sleep(300 * time.Millisecond) // longer than restartDelay
	waitRestarts(t, u.restarts, 0)
}

func TestUnpairRefusesAPairingSetByTheEnvironment(t *testing.T) {
	// Either variable alone names the pairing, so each is checked on its own.
	for _, name := range []string{"KELD_CTX_TOKEN", "KELD_CTX_ENDPOINT"} {
		t.Run(name, func(t *testing.T) {
			u := newUnpairH(t)
			writePairing(t)
			t.Setenv(name, "from-env")
			code, body := u.do(t)
			if code != http.StatusConflict || body["error"] != "pairing_set_by_env" {
				t.Fatalf("unpair with %s set: %d %v", name, code, body)
			}
			if _, err := os.Stat(paths.HookConfigPath()); err != nil {
				t.Fatalf("hook.json must survive a refused unpair: %v", err)
			}
			waitRestarts(t, u.restarts, 0)
		})
	}
}

func TestUnpairRefusesWhileASignInIsWaiting(t *testing.T) {
	u := newUnpairH(t)
	writePairing(t)
	if _, err := u.h.store.begin("http://a"); err != nil {
		t.Fatal(err)
	}
	code, body := u.do(t)
	if code != http.StatusConflict || body["error"] != "signin_in_progress" {
		t.Fatalf("unpair mid-sign-in: %d %v", code, body)
	}
	waitRestarts(t, u.restarts, 0)
}

func TestUnpairNeedsThePageSecret(t *testing.T) {
	u := newUnpairH(t)
	res, err := http.Post(u.h.srv.URL+"/v1/auth/unpair", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized && res.StatusCode != http.StatusForbidden {
		t.Fatalf("unpair without the secret: %d", res.StatusCode)
	}
}
