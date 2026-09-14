package ingress

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
)

func TestLedgerRouteRequiresSecret(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir()) // isolate: LedgerRoute is backed by a real ledger.Store
	store := ledger.New()
	srv := httptest.NewServer(DiscardHandler("s3cret", LedgerRoute(store)))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/v1/ledger")
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no secret: want 401, got %d", res.StatusCode)
	}
}

func TestLedgerRouteReturnsContractShape(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()

	at := time.Date(2026, 9, 4, 17, 34, 2, 0, time.UTC)
	k := ledger.BlockKey{Session: "0fc1a437347a8f95", Start: 1788543000}
	store.Cut(k, 1788544200, "idle", "budget", "claude_code", at)
	store.Measure(k, ledger.Measured{
		InputTokens: 2, OutputTokens: 277, CacheReadTokens: 26915,
		CacheCreationTokens: 86736, RequestTokens: 41000, Requests: 12,
		Model: "claude-opus-4-8", EstimateUSD: 1.84,
	}, at)
	store.Attribute(k, ledger.Attributed{ProjectID: "p_keld_signal", Method: ledger.MethodRepo}, ledger.ReasonNone, at)
	store.Sent(k, at)
	store.Failed(k, ledger.StageReceived, ledger.ReasonAtlasRejected, 401, at)
	store.SetHealth(ledger.Health{Key: ledger.HealthDaemon, Status: ledger.StatusOK, Detail: "2.5.0", At: at})
	store.CutPending("9eb2b3ffdeadbeef", ledger.ReasonSidecarOutdated, at)

	srv := httptest.NewServer(DiscardHandler("s3cret", LedgerRoute(store)))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/ledger", nil)
	req.Header.Set("x-keld-agent-secret", "s3cret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", res.StatusCode)
	}
	if ct := res.Header.Get("Content-Type"); ct != "application/json" {
		t.Fatalf("want application/json, got %q", ct)
	}

	var snap ledger.Snapshot
	if err := json.NewDecoder(res.Body).Decode(&snap); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if snap.GeneratedAt == "" {
		t.Fatal("generated_at missing")
	}
	if len(snap.Health) != 1 || snap.Health[0].Key != "daemon" {
		t.Fatalf("health: %#v", snap.Health)
	}
	if len(snap.Pending) != 1 || snap.Pending[0].Session != "9eb2b3ffdeadbeef" {
		t.Fatalf("pending: %#v", snap.Pending)
	}
	if len(snap.Blocks) != 1 {
		t.Fatalf("blocks: %#v", snap.Blocks)
	}
	b := snap.Blocks[0]
	if b.Key.Session != "0fc1a437347a8f95" || b.Key.Start != 1788543000 {
		t.Fatalf("block key: %#v", b.Key)
	}
	for _, stage := range []string{"cut", "measured", "attributed", "sent", "received"} {
		if _, ok := b.Cells[stage]; !ok {
			t.Fatalf("expected cell %q present, got %#v", stage, b.Cells)
		}
	}
	if b.Cells["received"]["status"] != "failed" || b.Cells["received"]["reason"] != "atlas_rejected" {
		t.Fatalf("received cell: %#v", b.Cells["received"])
	}
}

func TestLedgerRouteBadSinceIs400(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	srv := httptest.NewServer(DiscardHandler("s3cret", LedgerRoute(store)))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/ledger?since=not-a-number", nil)
	req.Header.Set("x-keld-agent-secret", "s3cret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad since: want 400, got %d", res.StatusCode)
	}
}

func TestLedgerRouteBadLimitIs400(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	srv := httptest.NewServer(DiscardHandler("s3cret", LedgerRoute(store)))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/ledger?limit=-1", nil)
	req.Header.Set("x-keld-agent-secret", "s3cret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("negative limit: want 400, got %d", res.StatusCode)
	}
}
