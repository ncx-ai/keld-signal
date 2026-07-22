package resolve

import (
	"os"
	"path/filepath"
	"testing"
)

const geminiFixture = `{"sessionId":"sess_123","projectHash":"abc123","startTime":"2026-07-21T10:00:00Z","lastUpdated":"2026-07-21T10:05:00Z","kind":"chat"}
{"$set":{"field":"value"}}
{"id":"msg-uuid-001","timestamp":"2026-07-21T10:00:05Z","type":"user","content":[{"text":"hello "},{"text":"world"}]}
{"id":"msg-uuid-002","timestamp":"2026-07-21T10:00:10Z","type":"gemini","content":"ok"}
{"id":"msg-uuid-003","timestamp":"2026-07-21T10:00:15Z","type":"user","content":[{"text":"second prompt"}]}
`

func writeGeminiFixture(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gemini-chat.jsonl")
	if err := os.WriteFile(p, []byte(geminiFixture), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGeminiReaderSource(t *testing.T) {
	r := NewGeminiReader()
	if r.Source() != "gemini" {
		t.Fatalf("source=%q, want gemini", r.Source())
	}
}

func TestGeminiReaderReadUserPrompt(t *testing.T) {
	r := NewGeminiReader()
	text, ok := r.Read(writeGeminiFixture(t), "msg-uuid-001")
	if !ok || text != "hello world" {
		t.Fatalf("read msg-uuid-001: %q ok=%v, want (hello world,true)", text, ok)
	}
}

func TestGeminiReaderReadSecondUserPrompt(t *testing.T) {
	r := NewGeminiReader()
	text, ok := r.Read(writeGeminiFixture(t), "msg-uuid-003")
	if !ok || text != "second prompt" {
		t.Fatalf("read msg-uuid-003: %q ok=%v, want (second prompt,true)", text, ok)
	}
}

func TestGeminiReaderSkipsSetLine(t *testing.T) {
	r := NewGeminiReader()
	// The $set line should be skipped and not found
	_, ok := r.Read(writeGeminiFixture(t), "msg-uuid-002")
	if ok {
		t.Fatal("gemini type (not user) must not be found")
	}
}

func TestGeminiReaderNotFoundForMissingID(t *testing.T) {
	r := NewGeminiReader()
	_, ok := r.Read(writeGeminiFixture(t), "msg-uuid-999")
	if ok {
		t.Fatal("missing id must return ok=false")
	}
}

func TestGeminiReaderNotFoundForMetaLine(t *testing.T) {
	r := NewGeminiReader()
	// Meta line has no type field and no id field matching our format
	_, ok := r.Read(writeGeminiFixture(t), "sess_123")
	if ok {
		t.Fatal("meta line must not be found")
	}
}

func TestGeminiReaderEmptyContentNotFound(t *testing.T) {
	fixture := `{"sessionId":"sess_123","projectHash":"abc123","startTime":"2026-07-21T10:00:00Z","lastUpdated":"2026-07-21T10:00:00Z","kind":"chat"}
{"id":"msg-uuid-empty","timestamp":"2026-07-21T10:00:05Z","type":"user","content":[]}
{"id":"msg-uuid-valid","timestamp":"2026-07-21T10:00:10Z","type":"user","content":[{"text":"hello"}]}
`
	p := filepath.Join(t.TempDir(), "gemini-empty.jsonl")
	if err := os.WriteFile(p, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewGeminiReader()
	_, ok := r.Read(p, "msg-uuid-empty")
	if ok {
		t.Fatal("empty content must return ok=false")
	}
	// Valid one should still work
	text, ok := r.Read(p, "msg-uuid-valid")
	if !ok || text != "hello" {
		t.Fatalf("msg-uuid-valid: %q ok=%v, want (hello,true)", text, ok)
	}
}

func TestGeminiReaderRecentUserPrompts(t *testing.T) {
	r := NewGeminiReader()
	// Exclude msg-uuid-003 (current), should get msg-uuid-001 newest-first
	got := r.RecentUserPrompts(writeGeminiFixture(t), "msg-uuid-003", 5)
	if len(got) != 1 || got[0] != "hello world" {
		t.Fatalf("recent (excluding current): %v, want [hello world]", got)
	}
}

func TestGeminiReaderRecentUserPromptsNewestFirst(t *testing.T) {
	fixture := `{"sessionId":"sess_123","projectHash":"abc123","startTime":"2026-07-21T10:00:00Z","lastUpdated":"2026-07-21T10:00:00Z","kind":"chat"}
{"id":"msg-uuid-001","timestamp":"2026-07-21T10:00:05Z","type":"user","content":[{"text":"first"}]}
{"id":"msg-uuid-002","timestamp":"2026-07-21T10:00:10Z","type":"user","content":[{"text":"second"}]}
{"id":"msg-uuid-003","timestamp":"2026-07-21T10:00:15Z","type":"user","content":[{"text":"third"}]}
`
	p := filepath.Join(t.TempDir(), "gemini-recent.jsonl")
	if err := os.WriteFile(p, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewGeminiReader()
	// Exclude msg-uuid-003 (current), should get [second, first] newest-first
	got := r.RecentUserPrompts(p, "msg-uuid-003", 5)
	if len(got) != 2 || got[0] != "second" || got[1] != "first" {
		t.Fatalf("recent newest-first: %v, want [second first]", got)
	}
}

func TestGeminiReaderToleratesMalformedLines(t *testing.T) {
	fixture := `{"sessionId":"sess_123","projectHash":"abc123","startTime":"2026-07-21T10:00:00Z","lastUpdated":"2026-07-21T10:00:00Z","kind":"chat"}
not json at all
{"id":"msg-uuid-001","timestamp":"2026-07-21T10:00:05Z","type":"user","content":[{"text":"valid"}]}
{bad json
`
	p := filepath.Join(t.TempDir(), "gemini-malformed.jsonl")
	if err := os.WriteFile(p, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewGeminiReader()
	text, ok := r.Read(p, "msg-uuid-001")
	if !ok || text != "valid" {
		t.Fatalf("read with malformed lines: %q ok=%v, want (valid,true)", text, ok)
	}
}

func TestResolveGeminiSource(t *testing.T) {
	text, ok := Resolve("gemini", writeGeminiFixture(t), "msg-uuid-001", "")
	if !ok || text != "hello world" {
		t.Fatalf("resolve gemini: %q ok=%v, want (hello world,true)", text, ok)
	}
}

func TestGeminiReaderSkipsSetLineInRecentUserPrompts(t *testing.T) {
	fixture := `{"sessionId":"sess_123","projectHash":"abc123","startTime":"2026-07-21T10:00:00Z","lastUpdated":"2026-07-21T10:00:00Z","kind":"chat"}
{"id":"msg-uuid-001","timestamp":"2026-07-21T10:00:05Z","type":"user","content":[{"text":"first"}]}
{"$set":{"field":"value"}}
{"id":"msg-uuid-003","timestamp":"2026-07-21T10:00:15Z","type":"user","content":[{"text":"second"}]}
`
	p := filepath.Join(t.TempDir(), "gemini-set-skip.jsonl")
	if err := os.WriteFile(p, []byte(fixture), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewGeminiReader()
	// The $set line should be skipped; recent should only include "first"
	got := r.RecentUserPrompts(p, "msg-uuid-003", 5)
	if len(got) != 1 || got[0] != "first" {
		t.Fatalf("recent (skip $set): %v, want [first]", got)
	}
}
