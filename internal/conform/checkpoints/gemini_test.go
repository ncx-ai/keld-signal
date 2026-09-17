package checkpoints

import (
	"os"
	"path/filepath"
	"testing"
)

// ⚠️ **THE CHECKPOINT MUST READ THE ID THE DAEMON PUBLISHES, OR IT PROVES
// NOTHING.** Gemini's correlation id is NOT the chat record's uuid — that is a
// random value appearing in no telemetry, so keying on it leaves every
// enrichment orphaned. It is "<sessionId>########<0-based ordinal among genuine
// user prompts>", which is what the watcher emits and what Atlas joins on.
//
// ⚠️ **AND THIS FILE'S OLD FIXTURE WAS INTRODUCED WITH THE WORDS "A real Gemini
// chat file" WHILE BEING NOTHING OF THE KIND**: JSONL, a session-meta first
// line, `$set` mutation records. Gemini writes ONE JSON DOCUMENT with the
// session id at the top and the turns in a `messages` array, and `$set` records
// do not exist in any of the 55 real chat files measured on a developer
// machine. The checkpoint walked for `.jsonl`, found nothing, and reported "0
// transcript(s)" while the chat sat in the directory it had just walked — and
// the daemon's watcher had the identical bug, so no lane contradicted it.
//
// Both halves now read through internal/geminichat, so the id the checkpoint
// compares cannot drift from the id the daemon publishes.
func writeGeminiSession(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session-2026-09-16T10-00-sessabc1.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGeminiPromptIDsMatchWhatTheWatcherEmits(t *testing.T) {
	path := writeGeminiSession(t, `{
      "sessionId":"sess-abc",
      "startTime":"2026-09-16T10:00:00Z",
      "messages":[
        {"id":"u1","type":"user","content":[{"text":"first"}]},
        {"id":"a1","type":"gemini","content":"hi"},
        {"id":"u2","type":"user","content":"second"}
      ]}`)

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

// An empty-text user turn is not a prompt. Counting it would shift every later
// ordinal by one and silently mismatch the whole file.
func TestGeminiEmptyPromptTakesNoOrdinal(t *testing.T) {
	path := writeGeminiSession(t, `{
      "sessionId":"s1",
      "messages":[
        {"id":"e","type":"user","content":[{"text":""}]},
        {"id":"b","type":"user","content":"   "},
        {"id":"u","type":"user","content":[{"text":"real"}]}
      ]}`)
	got := promptIDsIn(path, "gemini_cli")
	if len(got) != 1 || got[0] != "s1########0" {
		t.Fatalf("got %v, want [s1########0] — a skipped turn must not consume an ordinal", got)
	}
}

// The walk has to admit what Gemini writes, and only for Gemini.
func TestTranscriptWalkAdmitsGeminisDocuments(t *testing.T) {
	for _, tc := range []struct {
		path, tool string
		want       bool
	}{
		{"/h/.gemini/tmp/p/chats/session-2026-09-16T10-00-a.json", "gemini_cli", true},
		{"/h/.gemini/tmp/p/chats/notes.json", "gemini_cli", false},
		{"/h/.claude/projects/x/a.jsonl", "claude_code", true},
		{"/h/.gemini/tmp/p/chats/session-a.json", "claude_code", false},
	} {
		if got := isTranscript(tc.path, tc.tool); got != tc.want {
			t.Errorf("isTranscript(%q, %q) = %v, want %v", tc.path, tc.tool, got, tc.want)
		}
	}
}
