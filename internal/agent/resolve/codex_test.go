package resolve

import (
	"os"
	"path/filepath"
	"testing"
)

const codexFixture = `{"timestamp":"2026-07-21T19:00:00Z","type":"session_meta","payload":{"id":"thread_1","session_id":"s1","cwd":"/work/proj","cli_version":"1.0.0"}}
{"timestamp":"2026-07-21T19:00:01Z","ordinal":5,"type":"event_msg","payload":{"type":"user_message","message":"refactor the auth module"}}
{"timestamp":"2026-07-21T19:00:02Z","ordinal":6,"type":"response_item","payload":{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}}
{"timestamp":"2026-07-21T19:00:03Z","ordinal":7,"type":"event_msg","payload":{"type":"token_count","input_tokens":10}}
{"timestamp":"2026-07-21T19:00:09Z","ordinal":12,"type":"event_msg","payload":{"type":"user_message","message":"now add tests"}}
`

func writeCodexFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout-2026-07-21.jsonl")
	if err := os.WriteFile(p, []byte(codexFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCodexReaderReadByOrdinal(t *testing.T) {
	r := NewCodexReader()
	if r.Source() != "codex" {
		t.Fatalf("source=%q", r.Source())
	}
	text, ok := r.Read(writeCodexFixture(t), "thread_1#5")
	if !ok || text != "refactor the auth module" {
		t.Fatalf("read #5: %q ok=%v", text, ok)
	}
	text, ok = r.Read(writeCodexFixture(t), "thread_1#12")
	if !ok || text != "now add tests" {
		t.Fatalf("read #12: %q ok=%v", text, ok)
	}
	// non-user_message ordinal → not found
	if _, ok := r.Read(writeCodexFixture(t), "thread_1#6"); ok {
		t.Fatal("ordinal 6 is a response_item, must not resolve")
	}
}

func TestCodexReaderRecentUserPrompts(t *testing.T) {
	r := NewCodexReader()
	got := r.RecentUserPrompts(writeCodexFixture(t), "thread_1#12", 5) // exclude current (#12)
	if len(got) != 1 || got[0] != "refactor the auth module" {
		t.Fatalf("recent=%v", got)
	}
}

func TestResolveCodexSource(t *testing.T) {
	text, ok := Resolve("codex", writeCodexFixture(t), "thread_1#5", "")
	if !ok || text != "refactor the auth module" {
		t.Fatalf("resolve codex: %q ok=%v", text, ok)
	}
}

// codexFixtureWithGarbage is the base fixture with a malformed, non-JSON line
// spliced in. Read must not panic on it and must still resolve valid lines.
const codexFixtureWithGarbage = `{"timestamp":"2026-07-21T19:00:00Z","type":"session_meta","payload":{"id":"thread_1","session_id":"s1","cwd":"/work/proj","cli_version":"1.0.0"}}
not even json {{{
{"timestamp":"2026-07-21T19:00:01Z","ordinal":5,"type":"event_msg","payload":{"type":"user_message","message":"refactor the auth module"}}
`

func writeCodexFixtureWithGarbage(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "rollout-garbage.jsonl")
	if err := os.WriteFile(p, []byte(codexFixtureWithGarbage), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCodexReaderMalformedLineIgnored verifies a garbage (non-JSON) line in the
// rollout file doesn't panic Read and doesn't block resolution of a valid line
// elsewhere in the file.
func TestCodexReaderMalformedLineIgnored(t *testing.T) {
	r := NewCodexReader()
	path := writeCodexFixtureWithGarbage(t)
	text, ok := r.Read(path, "thread_1#5")
	if !ok || text != "refactor the auth module" {
		t.Fatalf("read with garbage line present: %q ok=%v", text, ok)
	}
}

// TestCodexReaderReadNoHash verifies a promptID with no '#' separator is
// rejected (ok=false) without panicking.
func TestCodexReaderReadNoHash(t *testing.T) {
	r := NewCodexReader()
	path := writeCodexFixture(t)
	if text, ok := r.Read(path, "thread_1_no_separator"); ok {
		t.Fatalf("expected ok=false for promptID with no '#', got %q", text)
	}
}

// TestCodexReaderReadNonNumericOrdinal verifies a promptID whose ordinal
// suffix isn't a valid integer is rejected (ok=false) without panicking.
func TestCodexReaderReadNonNumericOrdinal(t *testing.T) {
	r := NewCodexReader()
	path := writeCodexFixture(t)
	if text, ok := r.Read(path, "thread_1#not-a-number"); ok {
		t.Fatalf("expected ok=false for non-numeric ordinal, got %q", text)
	}
}

const codexFixtureEmptyMessage = `{"timestamp":"2026-07-21T19:00:01Z","ordinal":0,"type":"event_msg","payload":{"type":"user_message","message":""}}
{"timestamp":"2026-07-21T19:00:02Z","ordinal":1,"type":"event_msg","payload":{"type":"user_message","message":"hello"}}
`

// TestCodexReaderEmptyMessageNotFound verifies an empty user_message text is
// treated as not-found in both the Read and RecentUserPrompts paths, matching
// the Claude reader's extractText (s != "") semantics.
func TestCodexReaderEmptyMessageNotFound(t *testing.T) {
	r := NewCodexReader()
	p := filepath.Join(t.TempDir(), "rollout-empty.jsonl")
	if err := os.WriteFile(p, []byte(codexFixtureEmptyMessage), 0o600); err != nil {
		t.Fatal(err)
	}

	if text, ok := r.Read(p, "thread_1#0"); ok {
		t.Fatalf("expected ok=false for empty message, got %q", text)
	}

	// currentPromptID "thread_1#99" doesn't match either ordinal, so the
	// empty-message line (ordinal 0) must still be excluded from the results
	// because its text is empty, not because it collides with currentOrdinal.
	got := r.RecentUserPrompts(p, "thread_1#99", 5)
	if len(got) != 1 || got[0] != "hello" {
		t.Fatalf("recent (empty message skipped) = %v", got)
	}
}

// TestCodexReaderRecentUserPromptsOrdinalZeroNotExcludedByBadCurrent verifies
// that an unparsable currentPromptID doesn't wrongly exclude a genuine
// ordinal-0 user message (currentOrdinal's zero value must not be mistaken
// for a successfully parsed 0).
func TestCodexReaderRecentUserPromptsOrdinalZeroNotExcludedByBadCurrent(t *testing.T) {
	r := NewCodexReader()
	fixture := `{"timestamp":"2026-07-21T19:00:00Z","ordinal":0,"type":"event_msg","payload":{"type":"user_message","message":"first message"}}
{"timestamp":"2026-07-21T19:00:01Z","ordinal":1,"type":"event_msg","payload":{"type":"user_message","message":"second message"}}
`
	p := filepath.Join(t.TempDir(), "rollout-ordinal-zero.jsonl")
	if err := os.WriteFile(p, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}

	// currentPromptID has no parseable ordinal at all.
	got := r.RecentUserPrompts(p, "unparsable-current-id", 5)
	if len(got) != 2 {
		t.Fatalf("expected both messages (ordinal-0 not wrongly excluded), got %v", got)
	}
}
