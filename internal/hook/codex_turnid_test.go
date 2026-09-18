package hook

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/spool"
)

// isolateHook points KELD_HOME at a temp dir so the spool, agent.json and
// hook.json are this test's own. With no agent.json the daemon is
// unreachable, which is the path that spools — and the spool is where a
// forwarded pointer can be read back without inventing a daemon.
func isolateHook(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	t.Setenv("KELD_CTX_ENDPOINT", "")
	t.Setenv("KELD_CTX_TOKEN", "")
	return home
}

// spooledPointers drains whatever the hook left on disk.
func spooledPointers(t *testing.T) []spool.Pointer {
	t.Helper()
	var got []spool.Pointer
	if _, err := spool.Drain(func(p spool.Pointer) error {
		got = append(got, p)
		return nil
	}); err != nil {
		t.Fatalf("drain spool: %v", err)
	}
	return got
}

// codexHookPayload returns the captured fixture with the given keys overridden
// (a nil value deletes the key), so every case below starts from a real
// payload rather than an invented one.
func codexHookPayload(t *testing.T, event string, over map[string]any) string {
	t.Helper()
	p := readCodexHookFixture(t, event)
	for k, v := range over {
		if v == nil {
			delete(p, k)
			continue
		}
		p[k] = v
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestCodexUserPromptSubmitYieldsTurnPointer is AC-9's hook half: the captured
// Codex 0.153.4 payload — which carries `turn_id` and no `prompt_id` — yields
// exactly one pointer identified as `<session_id>#<turn_id>`.
func TestCodexUserPromptSubmitYieldsTurnPointer(t *testing.T) {
	isolateHook(t)
	fixture := readCodexHookFixture(t, "UserPromptSubmit")
	session, _ := fixture["session_id"].(string)
	turn, _ := fixture["turn_id"].(string)
	wantID := session + "#" + turn

	if code := Run("codex", strings.NewReader(codexHookPayload(t, "UserPromptSubmit", nil)), io.Discard, time.Now()); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}

	got := spooledPointers(t)
	if len(got) != 1 {
		t.Fatalf("got %d pointers, want 1", len(got))
	}
	p := got[0]
	if p.Correlation.ID != wantID {
		t.Errorf("corr id = %q, want %q", p.Correlation.ID, wantID)
	}
	if p.Correlation.SessionID != session {
		t.Errorf("session id = %q, want %q", p.Correlation.SessionID, session)
	}
	if p.Correlation.Scheme != "prompt_id" {
		t.Errorf("scheme = %q, want prompt_id", p.Correlation.Scheme)
	}
	if p.Source.ID != "codex" || p.Source.Origin != "hook" {
		t.Errorf("source = %+v", p.Source)
	}
	if p.Pointer == nil {
		t.Fatal("pointer is nil")
	}
	if want, _ := fixture["transcript_path"].(string); p.Pointer.TranscriptPath != want {
		t.Errorf("transcript path = %q, want %q", p.Pointer.TranscriptPath, want)
	}
	if want, _ := fixture["cwd"].(string); p.Pointer.Cwd != want {
		t.Errorf("cwd = %q, want %q", p.Pointer.Cwd, want)
	}
	if p.Pointer.PromptID != wantID {
		t.Errorf("pointer prompt id = %q, want %q", p.Pointer.PromptID, wantID)
	}
	if p.Inline != nil {
		t.Errorf("pointer carries inline text: %+v", p.Inline)
	}
}

// TestPromptIDWinsOverTurnID: turn_id is the FALLBACK. A tool that grows a
// real prompt_id must not have it silently replaced by a synthesised id — the
// two would name the same prompt differently and Atlas joins on the id.
func TestPromptIDWinsOverTurnID(t *testing.T) {
	isolateHook(t)
	body := codexHookPayload(t, "UserPromptSubmit", map[string]any{"prompt_id": "real-prompt-id"})
	if code := Run("codex", strings.NewReader(body), io.Discard, time.Now()); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	got := spooledPointers(t)
	if len(got) != 1 {
		t.Fatalf("got %d pointers, want 1", len(got))
	}
	if got[0].Correlation.ID != "real-prompt-id" {
		t.Fatalf("corr id = %q, want the payload's own prompt_id", got[0].Correlation.ID)
	}
}

// TestNeitherIDIsSilentExitZero: SessionStart carries no turn at all, so it
// names no prompt. Exit 0, nothing forwarded, nothing spooled — a pointer to
// nothing is worse than no pointer, because the daemon would resolve it to an
// empty prompt and publish that.
func TestNeitherIDIsSilentExitZero(t *testing.T) {
	isolateHook(t)
	if code := Run("codex", strings.NewReader(codexHookPayload(t, "SessionStart", nil)), io.Discard, time.Now()); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if got := spooledPointers(t); len(got) != 0 {
		t.Fatalf("SessionStart spooled %d pointers, want 0: %+v", len(got), got)
	}

	// Same when a turn_id is present but the session id is not: the id would
	// be "#<turn>", which is not unique across sessions.
	isolateHook(t)
	body := codexHookPayload(t, "UserPromptSubmit", map[string]any{"session_id": nil})
	if code := Run("codex", strings.NewReader(body), io.Discard, time.Now()); code != 0 {
		t.Fatalf("exit %d, want 0", code)
	}
	if got := spooledPointers(t); len(got) != 0 {
		t.Fatalf("session-less payload spooled %d pointers, want 0: %+v", len(got), got)
	}
}

// TestSpooledPointerHasNoTextKey is the schema pin: the hook reads the prompt
// field of NO tool, and a sentinel planted there must appear in no output
// stream and in no byte of the spool. The Codex payload is the first that
// carries the prompt inline, so this is the first time it could have leaked.
func TestSpooledPointerHasNoTextKey(t *testing.T) {
	home := isolateHook(t)
	const sentinel = "SENTINEL-PROMPT-TEXT-MUST-NOT-CROSS"
	body := codexHookPayload(t, "UserPromptSubmit", map[string]any{"prompt": sentinel})

	var stderr bytes.Buffer
	stdout := captureStdout(t, func() {
		if code := Run("codex", strings.NewReader(body), &stderr, time.Now()); code != 0 {
			t.Fatalf("exit %d, want 0", code)
		}
	})

	if strings.Contains(stdout, sentinel) {
		t.Error("sentinel reached stdout")
	}
	if strings.Contains(stderr.String(), sentinel) {
		t.Error("sentinel reached stderr")
	}

	// Every byte under KELD_HOME — the spool database included, since it is
	// one file and a JSON body inside it is not reachable through the type.
	if found := grepTree(t, home, sentinel); found != "" {
		t.Errorf("sentinel found on disk in %s", found)
	}

	// And the pointer itself carries no text-bearing field.
	got := spooledPointers(t)
	if len(got) != 1 {
		t.Fatalf("got %d pointers, want 1", len(got))
	}
	raw, err := json.Marshal(got[0])
	if err != nil {
		t.Fatal(err)
	}
	var generic map[string]any
	if err := json.Unmarshal(raw, &generic); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"prompt", "text", "message", "inline"} {
		if _, ok := generic[forbidden]; ok {
			t.Errorf("pointer carries a %q key", forbidden)
		}
	}
}

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	os.Stdout = old
	w.Close()
	return <-done
}

// grepTree returns the first file under root whose bytes contain needle.
func grepTree(t *testing.T, root, needle string) string {
	t.Helper()
	var hit string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || hit != "" {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil
		}
		if bytes.Contains(b, []byte(needle)) {
			hit = p
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return hit
}
