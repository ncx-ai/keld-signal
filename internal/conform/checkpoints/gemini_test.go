package checkpoints

import (
	"os"
	"path/filepath"
	"testing"
)

// ⚠️ **THE CHECKPOINT MUST READ THE ID THE DAEMON PUBLISHES, OR IT PROVES
// NOTHING.** Gemini's correlation id is NOT the chat record's UUID — that is a
// random value that never appears in its telemetry, so keying on it leaves
// every enrichment orphaned. It is "<sessionId>########<0-based ordinal among
// genuine user prompts>", which is what internal/agent/watch/gemini.go emits
// and therefore what Atlas joins on.
//
// This mirrors that rule. If the two drift, `pointer` reports "no enrichment
// matched" — which is exactly what a real break looks like.
func TestGeminiPromptIDsMatchWhatTheWatcherEmits(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	// A real Gemini chat file: a meta line with the session id and no type, a
	// $set MUTATION that must be skipped, two genuine user prompts, and an
	// assistant turn between them.
	body := `{"sessionId":"sess-abc","startTime":"2026-09-16T10:00:00Z"}
{"id":"u1","sessionId":"sess-abc","type":"user","content":[{"text":"first"}]}
{"id":"a1","sessionId":"sess-abc","type":"gemini","content":[{"text":"hi"}]}
{"id":"m1","sessionId":"sess-abc","type":"user","$set":{"x":1},"content":[{"text":"ignored"}]}
{"id":"u2","sessionId":"sess-abc","type":"user","content":[{"text":"second"}]}
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := promptIDsIn(path, "gemini_cli")
	want := []string{"sess-abc########0", "sess-abc########1"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("id %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A $set mutation and an empty-text user line are both NOT prompts. Counting
// either would shift every later ordinal by one and silently mismatch the whole
// file.
func TestGeminiSkipsMutationsAndEmptyPrompts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	body := `{"sessionId":"s1"}
{"id":"m","sessionId":"s1","type":"user","$set":{"a":1},"content":[{"text":"x"}]}
{"id":"e","sessionId":"s1","type":"user","content":[{"text":""}]}
{"id":"u","sessionId":"s1","type":"user","content":[{"text":"real"}]}
`
	os.WriteFile(path, []byte(body), 0o600)
	got := promptIDsIn(path, "gemini_cli")
	if len(got) != 1 || got[0] != "s1########0" {
		t.Fatalf("got %v, want [s1########0] — a skipped line must not consume an ordinal", got)
	}
}
