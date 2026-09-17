package geminichat

import (
	"os"
	"path/filepath"
	"testing"
)

// ⚠️ **THE FIXTURE IS A REAL GEMINI 0.37.1 CHAT FILE**, captured from a
// conformance run (its content is the harness's own scripted prompt and the
// mock model's reply, so nothing personal is committed), with two turns added
// to carry the second content shape and an empty user turn.
//
// This matters more than the assertions do. The previous Gemini tests passed
// against hand-written JSONL — a format Gemini has never written — so the whole
// lane was green and dead at the same time. A fixture that does not resemble
// production is why that could happen; it is the same lesson the prompt-id seam
// records in AGENTS.md.
const fixture = "testdata/session-real-0.37.1.json"

func TestReadsARealGeminiChatFile(t *testing.T) {
	s, ok := Read(fixture)
	if !ok {
		t.Fatal("a real Gemini chat file did not parse — the lane this package " +
			"exists for is broken again")
	}
	if s.ID != "219a0a4b-89a2-43d1-818e-b762adc7bb98" {
		t.Errorf("session id = %q, want the document's top-level sessionId", s.ID)
	}
	if len(s.Prompts) != 2 {
		t.Fatalf("got %d prompts, want 2 genuine user turns: %+v", len(s.Prompts), s.Prompts)
	}
	// The array-of-blocks shape (4 of 262 real messages) and the bare-string
	// shape (258 of 262) must both resolve.
	if s.Prompts[0].Text != "reply with one word" {
		t.Errorf("block-shaped content = %q", s.Prompts[0].Text)
	}
	if s.Prompts[1].Text != "and one more" {
		t.Errorf("string-shaped content = %q", s.Prompts[1].Text)
	}
	// Ordinals count GENUINE PROMPTS, so the whitespace-only user turn must not
	// consume one. If it did, every later prompt would resolve to the text of
	// the one before it.
	if s.Prompts[0].Ordinal != 0 || s.Prompts[1].Ordinal != 1 {
		t.Errorf("ordinals = %d,%d — want 0,1 counted over genuine prompts only",
			s.Prompts[0].Ordinal, s.Prompts[1].Ordinal)
	}
	if got := s.CorrID(1); got != s.ID+"########1" {
		t.Errorf("corr id = %q, want the id Gemini's OTEL reports", got)
	}
}

// An unreadable or non-chat file must be distinguishable from a session with no
// prompts: the first is "we could not look", the second is a real answer.
func TestReadRefusesRatherThanReturningAnEmptySession(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ name, body string }{
		{"not-json", "this is not json at all"},
		{"no-session-id", `{"messages":[{"id":"x","type":"user","content":"hi"}]}`},
	} {
		p := filepath.Join(dir, tc.name+".json")
		if err := os.WriteFile(p, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := Read(p); ok {
			t.Errorf("%s: parsed as a chat file; a caller would read it as a session with no prompts", tc.name)
		}
	}
	if _, ok := Read(filepath.Join(dir, "absent.json")); ok {
		t.Error("a missing file must not parse")
	}
}

// The walk filter has to admit what Gemini writes and nothing else — the same
// directory holds the project marker and, one level up, a vendored ripgrep.
func TestIsChatFile(t *testing.T) {
	for path, want := range map[string]bool{
		"/h/.gemini/tmp/p/chats/session-2026-09-17T11-54-219a0a4b.json": true,
		"/h/.gemini/tmp/p/chats/notes.json":                             false,
		"/h/.gemini/tmp/p/.project_root":                                false,
		"/h/.claude/projects/x/abc.jsonl":                               false,
	} {
		if got := IsChatFile(path); got != want {
			t.Errorf("IsChatFile(%q) = %v, want %v", path, got, want)
		}
	}
}
