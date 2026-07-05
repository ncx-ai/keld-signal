package hook

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

func TestForwardPostsPointerWithSecret(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	var gotSecret, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSecret = r.Header.Get("x-keld-agent-secret")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(202)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	if err := agentcfg.Write(agentcfg.Info{Port: port, Secret: "sek"}); err != nil {
		t.Fatal(err)
	}

	forwardToAgent("claude_code", "S1", "P1", "/t/x.jsonl", "/cwd")

	if gotSecret != "sek" {
		t.Fatalf("secret header = %q", gotSecret)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(gotBody), &body); err != nil {
		t.Fatalf("body not json: %s", gotBody)
	}
	if body["correlation"].(map[string]any)["id"] != "P1" {
		t.Fatalf("correlation id wrong: %s", gotBody)
	}
}

func TestForwardNoopWhenAgentAbsent(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	// Must not panic or block when agent.json is missing.
	forwardToAgent("claude_code", "S1", "P1", "/t", "/cwd")
}

func TestForwardLogsNon2xxWithoutPromptText(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		w.WriteHeader(500)
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	if err := agentcfg.Write(agentcfg.Info{Port: port, Secret: "sek"}); err != nil {
		t.Fatal(err)
	}

	forwardToAgent("claude_code", "S1", "P1", "/secret/transcript.jsonl", "/cwd")

	data, err := os.ReadFile(paths.DebugLogPath())
	if err != nil {
		t.Fatalf("expected a debug log entry: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, "500") {
		t.Fatalf("debug log should record the status: %q", s)
	}
	if strings.Contains(s, "/secret/transcript.jsonl") {
		t.Fatalf("debug log must not contain the transcript path/content: %q", s)
	}
}
