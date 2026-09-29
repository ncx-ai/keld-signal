package daemon

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

// The browser sign-in must be reachable on an UNPAIRED daemon — that is where
// it starts. The two page routes sit behind the secret; the callback does not.
func TestUnconfiguredDaemonMountsTheSignInRoutes(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_AUTH_NO_BROWSER", "1")
	srv := httptest.NewServer(onboardingHandler(settings.Load(), "s3cret"))
	defer srv.Close()

	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/v1/auth/start"},
		{http.MethodGet, "/v1/auth/state"},
	} {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, nil)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s %s without the secret: got %d, want 401 (mounted and gated)", c.method, c.path, res.StatusCode)
		}
		req, _ = http.NewRequest(c.method, srv.URL+c.path, nil)
		req.Header.Set("x-keld-agent-secret", "s3cret")
		res, err = http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusOK {
			t.Fatalf("%s %s with the secret: got %d, want 200", c.method, c.path, res.StatusCode)
		}
	}

	res, err := http.Get(srv.URL + "/auth/callback?state=x&pairing_code=y")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusBadRequest || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/html") {
		t.Fatalf("callback: got %d %q, want the fixed 400 refusal page", res.StatusCode, res.Header.Get("Content-Type"))
	}
}
