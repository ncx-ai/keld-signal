package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/spool"
)

// WS1 — COLLECT ALWAYS, PAIR TO SEND.
//
// ⚠️ **AN UNPAIRED MACHINE COLLECTED NOTHING, AND THAT WAS LEFT OVER FROM A
// DESIGN THAT NO LONGER EXISTS.** `Run` started nothing but the integrations
// detector until `awaitConfig` saw `~/.keld/hook.json`, so a machine between
// install and login had no telemetry proxy listening (every AI tool posting
// into a closed port), no transcript watcher, no block emitter and no
// enrichment. The gate dates from when the hook posted straight to Atlas and
// there genuinely was nothing to do without a token. Every lane now has a
// local store or a bounded spool, so collection needs Atlas for nothing —
// only sending does.

// constEndpoint adapts a fixed URL to the resolver every sender now takes. The
// resolver exists because the daemon builds its senders BEFORE the machine is
// paired (see pairing.go); a test that already knows its endpoint just answers
// the same string every time.
func constEndpoint(u string) func() string { return func() string { return u } }

// freePort reserves and releases a loopback port so the daemon under test can
// bind the telemetry proxy without colliding with a developer's real daemon on
// the fixed 14318.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a port: %v", err)
	}
	p := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	return p
}

// unpairedHome sets up an isolated KELD_HOME with no hook.json, a transcript
// the watcher will read, and a telemetry port of its own.
func unpairedHome(t *testing.T) (home, transcript string, telePort int) {
	t.Helper()
	// NOT t.TempDir: the daemon's own goroutines (the ledger, the reference
	// series) can still be writing under state/ when the test body returns, and
	// t.TempDir's RemoveAll FAILS THE TEST on a directory that grew during
	// cleanup. Best-effort removal instead — a leftover temp dir is not a
	// defect, and a flaky teardown masking a real result is.
	var err error
	home, err = os.MkdirTemp("", "keld-ws1-")
	if err != nil {
		t.Fatalf("temp home: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	t.Setenv("KELD_HOME", home)
	// ⚠️ HOME TOO, not just KELD_HOME. The transcript watcher's default roots
	// are resolved from the user's home (~/.claude/projects), so a test that
	// isolates only KELD_HOME backfills the DEVELOPER'S OWN transcripts —
	// measured here, 55 real prompts spooled by a test that had written one.
	t.Setenv("HOME", home)
	t.Setenv("KELD_CONFIG_POLL", "20ms")
	// ⚠️ OFF, because the detector edits the DEVELOPER'S OWN tool configs.
	// KELD_HOME isolates ~/.keld but not ~/.claude/settings.json, and a test
	// that rewrites the machine it runs on is a worse defect than the one it
	// checks for — the same rule teleproxy's TestMain already applies to its
	// state file.
	t.Setenv("KELD_AUTO_SETUP_INTEGRATIONS", "0")

	telePort = freePort(t)
	t.Setenv("KELD_TELEMETRY_PORT", fmt.Sprintf("%d", telePort))

	// Deterministic backend: no model is ever loaded, and with no sidecar
	// binary installed the readiness gate is trivially true (see
	// noAnalysisService), so a job reaches the publish step in a unit test.
	if err := os.WriteFile(filepath.Join(home, "agent-config.json"),
		[]byte(`{"ml_backend":"deterministic"}`), 0o600); err != nil {
		t.Fatalf("write agent-config.json: %v", err)
	}

	projects := filepath.Join(home, "projects", "proj")
	if err := os.MkdirAll(projects, 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}
	transcript = filepath.Join(projects, "session-ws1.jsonl")
	line, _ := json.Marshal(map[string]any{
		"type":      "user",
		"promptId":  "ws1-prompt-1",
		"uuid":      "ws1-uuid-1",
		"sessionId": "ws1-session",
		"cwd":       home,
		"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		"message":   map[string]any{"role": "user", "content": "hello from an unpaired machine"},
	})
	if err := os.WriteFile(transcript, append(line, '\n'), 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}

	t.Setenv("KELD_WATCH", "1")
	t.Setenv("KELD_WATCH_POLL", "20ms")
	t.Setenv("KELD_WATCH_BACKFILL", "1")
	t.Setenv("KELD_WATCH_ROOTS", "claude_code:"+filepath.Dir(projects))
	t.Setenv("KELD_WATCH_TELEMETRY", "off")
	// Keep the spool sweep brisk so the paired half does not wait 30s.
	t.Setenv("KELD_SPOOL_SWEEP", "100ms")
	return home, transcript, telePort
}

func waitForWS1(t *testing.T, what string, d time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestCollectorsRunBeforeThePairing is the first half of the invariant: with no
// hook.json at all the daemon must already be collecting.
func TestCollectorsRunBeforeThePairing(t *testing.T) {
	home, _, telePort := unpairedHome(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()

	waitForWS1(t, "agent.json", 10*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(home, "agent.json"))
		return err == nil
	})

	// 1. THE TELEMETRY PROXY IS LISTENING. `keld signal setup` writes this
	// address into every tool config, so a daemon that binds it only after
	// pairing leaves every already-configured tool posting into a closed port.
	waitForWS1(t, "the telemetry proxy to be listening", 10*time.Second, func() bool {
		c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", telePort), time.Second)
		if err != nil {
			return false
		}
		_ = c.Close()
		return true
	})

	// 2. THE WATCHER OFFERED THE PROMPT AND IT LANDED SOMEWHERE DURABLE. The
	// enrich spool is the durable path that already exists; nothing may be lost
	// merely because no token was available.
	waitForWS1(t, "the enrich spool to fill", 15*time.Second, func() bool {
		s, err := spool.Stats()
		return err == nil && s.Rows > 0
	})

	// 3. AND NOTHING THAT SENDS HAS STARTED. Collection does not need Atlas;
	// only delivery does.
	if sendersStarted.Load() {
		t.Fatal("the senders started on a machine with no pairing")
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v on a clean shutdown", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// TestPairingStartsTheSendersAndDeliversWhatWasHeld is the second half: the
// pairing arriving mid-run must start the senders with no restart, and
// everything collected while unpaired must then be delivered.
func TestPairingStartsTheSendersAndDeliversWhatWasHeld(t *testing.T) {
	home, _, _ := unpairedHome(t)

	var posted atomic.Int64
	atlas := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		posted.Add(1)
		w.Header().Set("content-type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer atlas.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx) }()

	waitForWS1(t, "the enrich spool to fill while unpaired", 20*time.Second, func() bool {
		s, err := spool.Stats()
		return err == nil && s.Rows > 0
	})
	before, err := spool.Stats()
	if err != nil {
		t.Fatalf("spool stats: %v", err)
	}
	if posted.Load() != 0 {
		t.Fatalf("an unpaired daemon reached Atlas %d times", posted.Load())
	}
	if _, err := agentcfg.Read(); err != nil {
		t.Fatalf("read agent.json: %v", err)
	}

	// THE PAIRING ARRIVES. No restart: awaitConfig is already polling.
	hookJSON := fmt.Sprintf(`{"endpoint":%q,"ingest_token":"ws1-token"}`, atlas.URL+"/v1/ingest")
	if err := os.WriteFile(filepath.Join(home, "hook.json"), []byte(hookJSON), 0o600); err != nil {
		t.Fatalf("write hook.json: %v", err)
	}

	waitForWS1(t, "the senders to start", 20*time.Second, func() bool { return sendersStarted.Load() })
	waitForWS1(t, "what was held to be delivered", 40*time.Second, func() bool {
		s, err := spool.Stats()
		return err == nil && s.Rows == 0 && posted.Load() > 0
	})

	after, err := spool.Stats()
	if err != nil {
		t.Fatalf("spool stats: %v", err)
	}
	t.Logf("spool rows before pairing=%d after=%d, Atlas received %d posts",
		before.Rows, after.Rows, posted.Load())

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v on a clean shutdown", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}
