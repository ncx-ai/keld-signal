package hook

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestNormalizeRemoteVariants(t *testing.T) {
	cases := map[string]string{
		"git@github.com:acme/api.git":     "github.com/acme/api",
		"https://github.com/acme/api.git": "github.com/acme/api",
		"https://user:tok@github.com/a/b": "github.com/a/b",
		"ssh://git@github.com/a/b.git":    "github.com/a/b",
	}
	for in, want := range cases {
		if got := NormalizeRemote(in); got != want {
			t.Errorf("NormalizeRemote(%q)=%q want %q", in, got, want)
		}
	}
}

func TestReadAttributesScalarsOnly(t *testing.T) {
	dir := t.TempDir()
	content := "[keld]\nteam = \"core\"\ncost = 5\n[keld.nested]\nx=1\n"
	if err := os.WriteFile(dir+"/.keld.toml", []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	a := ReadAttributes(dir)
	if a["team"] != "core" {
		t.Fatalf("expected team=core, got %q; attrs=%v", a["team"], a)
	}
	if a["cost"] != "5" {
		t.Fatalf("expected cost=5, got %q; attrs=%v", a["cost"], a)
	}
	if _, ok := a["nested"]; ok {
		t.Fatal("non-scalar leaked into attributes")
	}
}

func TestReadAttributesMissingFile(t *testing.T) {
	dir := t.TempDir()
	a := ReadAttributes(dir)
	if len(a) != 0 {
		t.Fatalf("expected empty map for missing file, got %v", a)
	}
}

func TestReadAttributesInvalidTOML(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/.keld.toml", []byte("not valid toml [[["), 0o644); err != nil {
		t.Fatal(err)
	}
	a := ReadAttributes(dir)
	if len(a) != 0 {
		t.Fatalf("expected empty map for invalid TOML, got %v", a)
	}
}

func TestChangedSinceLastDedup(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if !ChangedSinceLast("s1", "repo", map[string]string{"a": "1"}) {
		t.Fatal("first call should be changed")
	}
	if ChangedSinceLast("s1", "repo", map[string]string{"a": "1"}) {
		t.Fatal("identical second call should be unchanged")
	}
}

func TestChangedSinceLastDifferentAttrs(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if !ChangedSinceLast("sess2", "repo", map[string]string{"a": "1"}) {
		t.Fatal("first call should be changed")
	}
	// Different attributes → should be changed
	if !ChangedSinceLast("sess2", "repo", map[string]string{"a": "2"}) {
		t.Fatal("different attrs should be changed")
	}
}

func TestChangedSinceLastSessionIDSanitize(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	// Session IDs with slashes should not cause path traversal
	sessionID := "org/team/sess3"
	if !ChangedSinceLast(sessionID, "repo", nil) {
		t.Fatal("first call should be changed")
	}
	if ChangedSinceLast(sessionID, "repo", nil) {
		t.Fatal("identical second call should be unchanged")
	}
}

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
	t.Setenv("KELD_CTX_ENDPOINT", "")
	t.Setenv("KELD_CTX_TOKEN", "")
	// Malformed JSON should be treated as {} and still return 0
	if code := Run("claude_code", strings.NewReader("{not json"), io.Discard, time.Unix(0, 0).UTC()); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
}

func TestRunNoSessionID(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_CTX_ENDPOINT", "http://example.com")
	t.Setenv("KELD_CTX_TOKEN", "tok")
	// No session_id → return 0 without posting
	if code := Run("claude_code", strings.NewReader("{}"), io.Discard, time.Now()); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
}

func TestRunPostsAndDedups(t *testing.T) {
	var hits int
	var lastPayload map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &lastPayload)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_CTX_ENDPOINT", srv.URL)
	t.Setenv("KELD_CTX_TOKEN", "test-token")

	now := time.Date(2024, 1, 15, 12, 0, 0, 0, time.UTC)
	payload := `{"session_id":"test-sess-123","cwd":"/tmp"}`

	// First call: should POST
	code := Run("claude_code", strings.NewReader(payload), io.Discard, now)
	if code != 0 {
		t.Fatalf("first Run exit %d, want 0", code)
	}
	if hits != 1 {
		t.Fatalf("expected 1 POST after first Run, got %d", hits)
	}

	// Verify payload fields
	if lastPayload["session_id"] != "test-sess-123" {
		t.Errorf("session_id=%v want test-sess-123", lastPayload["session_id"])
	}
	if lastPayload["source"] != "claude_code" {
		t.Errorf("source=%v want claude_code", lastPayload["source"])
	}
	if lastPayload["ts"] == nil {
		t.Error("ts field missing")
	}

	// Second identical call: should dedup and NOT POST
	code = Run("claude_code", strings.NewReader(payload), io.Discard, now)
	if code != 0 {
		t.Fatalf("second Run exit %d, want 0", code)
	}
	if hits != 1 {
		t.Fatalf("expected still 1 POST after dedup, got %d", hits)
	}
}

func TestRunPostFailureNeverBlocks(t *testing.T) {
	// Server that returns 500
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_CTX_ENDPOINT", srv.URL)
	t.Setenv("KELD_CTX_TOKEN", "test-token")

	payload := `{"session_id":"fail-sess","cwd":"/tmp"}`
	var errBuf strings.Builder
	code := Run("claude_code", strings.NewReader(payload), &errBuf, time.Now())
	if code != 0 {
		t.Fatalf("Run exit %d on POST failure, want 0", code)
	}
	// In prod mode (not localhost), should write one-liner warning
	if !strings.Contains(errBuf.String(), "keld-context") {
		t.Errorf("expected keld-context warning on stderr, got: %q", errBuf.String())
	}
}

func TestIsDev(t *testing.T) {
	t.Setenv("KELD_CTX_DEBUG", "")
	if IsDev("https://atlas.keld.co") {
		t.Error("prod endpoint should not be dev")
	}
	if !IsDev("http://localhost:8080") {
		t.Error("localhost should be dev")
	}
	if !IsDev("http://127.0.0.1:8080") {
		t.Error("127.0.0.1 should be dev")
	}

	t.Setenv("KELD_CTX_DEBUG", "1")
	if !IsDev("https://atlas.keld.co") {
		t.Error("KELD_CTX_DEBUG=1 should force dev mode")
	}
	t.Setenv("KELD_CTX_DEBUG", "0")
	if IsDev("https://atlas.keld.co") {
		t.Error("KELD_CTX_DEBUG=0 should not force dev mode")
	}
}

func TestErrSummary(t *testing.T) {
	if got := errSummary(&httpStatusError{code: 500}); got != "HTTP 500" {
		t.Errorf("errSummary(HTTP 500)=%q want %q", got, "HTTP 500")
	}

	urlErr := &url.Error{
		Op:  "Post",
		URL: "http://example.com/v1/logs?token=secret",
		Err: errors.New("connection refused"),
	}
	got := errSummary(urlErr)
	if got != "connection refused" {
		t.Errorf("errSummary(url.Error)=%q want %q", got, "connection refused")
	}
	// Must not leak the URL, host, or token.
	for _, leak := range []string{"example.com", "token", "http://", "secret"} {
		if strings.Contains(got, leak) {
			t.Errorf("errSummary leaked %q in output: %q", leak, got)
		}
	}
}

func TestNormalizeRemoteNoOp(t *testing.T) {
	// Already normalized
	got := NormalizeRemote("github.com/acme/repo")
	if got != "github.com/acme/repo" {
		t.Errorf("NormalizeRemote(already normalized)=%q want github.com/acme/repo", got)
	}
}

// panicReader is a test io.Reader whose Read method always panics.
type panicReader struct{}

func (panicReader) Read([]byte) (int, error) { panic("boom") }

// TestRunRecoverFromPanic verifies that Run catches an unexpected panic and
// still returns 0, never blocking the host tool.
func TestRunRecoverFromPanic(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	// Set endpoint+token so Run gets past the config gate and actually reads stdin.
	t.Setenv("KELD_CTX_ENDPOINT", "http://localhost:19999") // no server; panic before dial
	t.Setenv("KELD_CTX_TOKEN", "test-token")

	code := Run("claude_code", panicReader{}, io.Discard, time.Now())
	if code != 0 {
		t.Fatalf("Run returned %d after panic; want 0 (never block host tool)", code)
	}
}
