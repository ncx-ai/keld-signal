package hook

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestRunNeverBlocksWithoutConfig(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_CTX_ENDPOINT", "")
	t.Setenv("KELD_CTX_TOKEN", "")
	if code := Run("claude_code", strings.NewReader("{}"), io.Discard, time.Unix(0, 0).UTC()); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
}

func TestRunNeverBlocksMalformedStdin(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	// Malformed JSON should be treated as {} and still return 0.
	if code := Run("claude_code", strings.NewReader("{not json"), io.Discard, time.Unix(0, 0).UTC()); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
}

func TestRunNoSessionID(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	// No session_id / prompt_id → nothing to forward, still return 0.
	if code := Run("claude_code", strings.NewReader("{}"), io.Discard, time.Now()); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
}

// panicReader is a test io.Reader whose Read method always panics.
type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("boom") }

// TestRunRecoverFromPanic verifies that Run catches an unexpected panic and
// still returns 0, never blocking the host tool.
func TestRunRecoverFromPanic(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	code := Run("claude_code", panicReader{}, io.Discard, time.Now())
	if code != 0 {
		t.Fatalf("Run returned %d after panic; want 0 (never block host tool)", code)
	}
}

// TestGeminiBeforeAgentHook verifies that hook.Run with Gemini BeforeAgent input
// (no prompt_id) exits 0, writes nothing to stdout (Gemini's strict-JSON
// requirement), and does not forward an enrichment pointer (forwardToAgent
// early-returns on empty promptID). No context event is POSTed anywhere.
func TestGeminiBeforeAgentHook(t *testing.T) {
	// Temp home so agent.json doesn't exist → no daemon forward attempted.
	t.Setenv("KELD_HOME", t.TempDir())

	// A server that would catch any stray POST (there must be none).
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	t.Setenv("KELD_CTX_ENDPOINT", srv.URL)
	t.Setenv("KELD_CTX_TOKEN", "test-token")

	// Gemini BeforeAgent input: no prompt_id field.
	geminiInput := `{
		"session_id":"gemini-session-123",
		"transcript_path":"/path/to/transcript.jsonl",
		"cwd":"/tmp",
		"hook_event_name":"BeforeAgent",
		"timestamp":"2025-07-22T00:00:00Z",
		"prompt":"test prompt"
	}`

	var stderrBuf strings.Builder

	// Capture stdout by redirecting os.Stdout.
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("failed to create pipe for stdout capture: %v", err)
	}
	os.Stdout = w

	code := Run("gemini", strings.NewReader(geminiInput), &stderrBuf, time.Now())

	os.Stdout = oldStdout
	w.Close()

	var capturedStdout strings.Builder
	_, _ = io.Copy(&capturedStdout, r)

	if code != 0 {
		t.Fatalf("Run returned %d, want 0", code)
	}
	if capturedStdout.Len() > 0 {
		t.Errorf("stdout should be empty for Gemini, got: %q", capturedStdout.String())
	}
	// No prompt_id → no enrichment pointer forwarded; and no context POST at all.
	if hits > 0 {
		t.Errorf("no POST should be made for a BeforeAgent event, got %d hits", hits)
	}
}
