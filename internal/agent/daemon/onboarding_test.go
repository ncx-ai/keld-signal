package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// TestUnconfiguredDaemonServesThePageAndTheConfigRoute is the regression this
// whole file exists for.
//
// Before onboarding.go, every listener was created after awaitConfig, so a
// machine with no hook.json served nothing at all: no agent.json for the app to
// find, no page, and no reachable POST /v1/config — the one route whose job is
// to onboard a machine. Pairing from the app was structurally impossible, and
// the failure looked like a working daemon, because the daemon WAS working. It
// was idling exactly as designed with no way to be told where Atlas is.
//
// The assertion is deliberately about the two surfaces a person uses, not about
// the internals: the page loads, and the config route answers something other
// than "no such route".
func TestUnconfiguredDaemonServesThePageAndTheConfigRoute(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if _, err := os.Stat(paths.HookConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("this test is only meaningful with no hook.json; stat gave %v", err)
	}

	h := onboardingHandler(settings.Load(), "s3cret")
	srv := httptest.NewServer(h)
	defer srv.Close()

	// The page.
	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("an unconfigured daemon must still serve the page, got %d", res.StatusCode)
	}
	if len(body) == 0 {
		t.Fatal("the page was empty")
	}

	// The config route. A malformed code is a 400 from the route itself, which
	// is what proves it is MOUNTED — a 404 here is the original defect, and a
	// 401 would mean the secret gate rejected us before the route was reached.
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/config", jsonBody(`{"code":""}`))
	req.Header.Set("x-keld-agent-secret", "s3cret")
	req.Header.Set("content-type", "application/json")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /v1/config: %v", err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		t.Fatal("POST /v1/config is not mounted on an unconfigured daemon — pairing from the app is impossible")
	}
	if res.StatusCode == http.StatusUnauthorized {
		t.Fatal("POST /v1/config rejected the agent secret on an unconfigured daemon")
	}

	// And settings, because the page reads it on load to decide what to render.
	req, _ = http.NewRequest(http.MethodGet, srv.URL+"/v1/settings", nil)
	req.Header.Set("x-keld-agent-secret", "s3cret")
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /v1/settings: %v", err)
	}
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/settings on an unconfigured daemon: %d", res.StatusCode)
	}
	// Send to Atlas defaults ON — absent means on — so a freshly installed
	// machine that pairs through the page publishes without touching a toggle.
	if out["send_to_atlas"] != true {
		t.Fatalf("send_to_atlas must default to true on a fresh machine, got %v", out["send_to_atlas"])
	}
}

// TestAgentJSONIsWrittenBeforeConfigArrives pins the OTHER half: the app finds
// the daemon by reading agent.json, so writing it after awaitConfig meant the
// app could not even locate the port to pair through.
func TestAgentJSONIsWrittenBeforeConfigArrives(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	t.Setenv("KELD_CONFIG_POLL", "20ms")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()

	// Run must publish agent.json while it is still blocked in awaitConfig.
	discovery := filepath.Join(home, "agent.json")
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(discovery); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("agent.json was never written on an unconfigured machine — the app cannot find the daemon to pair with")
		}
		time.Sleep(20 * time.Millisecond)
	}

	info, err := agentcfg.Read()
	if err != nil {
		t.Fatalf("read agent.json: %v", err)
	}
	if info.Port == 0 || info.Secret == "" {
		t.Fatalf("agent.json is incomplete: %+v", info)
	}

	// And the port actually answers, so this is a live listener rather than a
	// recorded number.
	conn, err := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", itoa(info.Port)), 3*time.Second)
	if err != nil {
		cancel()
		t.Fatalf("nothing is listening on the port agent.json advertises: %v", err)
	}
	conn.Close()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v on a clean shutdown", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func jsonBody(s string) io.Reader { return stringReader(s) }

type stringReader string

func (s stringReader) Read(p []byte) (int, error) {
	n := copy(p, s)
	if n == len(s) {
		return n, io.EOF
	}
	return n, nil
}
