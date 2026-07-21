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
