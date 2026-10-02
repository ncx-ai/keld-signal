package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/conform/mockatlas"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// A sign-in finished from the page must leave the daemon PAIRED by the time the
// page re-reads it, not one config poll later. The page polls /v1/auth/state,
// sees paired (hook.json is on disk), shows "Signed in as …" and reloads the
// ledger once — and until this the health strip under that green bar went on
// saying "Atlas not paired", because the watcher that adopts hook.json had not
// looked yet. The poll here is an hour, so only the sign-in's own announcement
// can explain the row moving.
func TestSignInPairsTheRunningDaemonAtOnce(t *testing.T) {
	unpairedHome(t)
	t.Setenv("KELD_CONFIG_POLL", "1h")
	t.Setenv("KELD_AUTH_NO_BROWSER", "1")
	t.Setenv("KELD_WATCH", "0")
	paths.SetAPIBaseOverride("")
	mock, err := mockatlas.New(mockatlas.Options{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ms := httptest.NewServer(mock)
	defer ms.Close()
	t.Setenv("KELD_API_URL", ms.URL)
	t.Setenv("KELD_ATLAS_WEB_URL", ms.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			t.Error("Run did not return after its context was cancelled")
		}
	}()

	var base, secret string
	waitForWS1(t, "agent.json", 10*time.Second, func() bool {
		info, err := agentcfg.Read()
		if err != nil || info == nil || info.Port == 0 || info.Secret == "" {
			return false
		}
		base, secret = fmt.Sprintf("http://127.0.0.1:%d", info.Port), info.Secret
		return true
	})
	atlasRow := func() (ledger.HealthEntry, bool) {
		req, _ := http.NewRequest(http.MethodGet, base+"/v1/ledger", nil)
		req.Header.Set("x-keld-agent-secret", secret)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			return ledger.HealthEntry{}, false
		}
		defer res.Body.Close()
		var snap ledger.Snapshot
		if res.StatusCode != http.StatusOK || json.NewDecoder(res.Body).Decode(&snap) != nil {
			return ledger.HealthEntry{}, false
		}
		h, ok := healthByKey(snap)["atlas"]
		return h, ok
	}
	waitForWS1(t, "the atlas row to say not_paired before the sign-in", 10*time.Second, func() bool {
		h, ok := atlasRow()
		return ok && h.Detail == string(ledger.ReasonNotPaired)
	})

	req, _ := http.NewRequest(http.MethodPost, base+"/v1/auth/start", nil)
	req.Header.Set("x-keld-agent-secret", secret)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var start struct {
		AuthorizeURL string `json:"authorize_url"`
	}
	_ = json.NewDecoder(res.Body).Decode(&start)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || start.AuthorizeURL == "" {
		t.Fatalf("start: %d %+v", res.StatusCode, start)
	}
	// The "browser": follow the mock's redirect back to /auth/callback.
	res, err = http.Get(start.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("the sign-in round trip ended at %d", res.StatusCode)
	}

	// The page's first poll lands within a second of the callback; give the
	// daemon that, plus slack for a loaded CI box — far inside the hour.
	waitForWS1(t, "the atlas row to stop saying not_paired", 3*time.Second, func() bool {
		h, ok := atlasRow()
		return ok && h.Detail != string(ledger.ReasonNotPaired) && sendersStarted.Load()
	})
}
