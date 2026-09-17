package resolve

import (
	"os"
	"path/filepath"
	"testing"
)

// ⚠️ **EVERY FIXTURE IN THIS FILE USED TO BE JSONL, AND GEMINI HAS NEVER
// WRITTEN JSONL.** The reader decoded one JSON object per line — session meta
// first, `$set` mutation lines, one object per turn — and the tests fed it
// exactly that, so the suite was green while the lane could not read a single
// real chat file. Measured before the rewrite: 55 real Gemini chat files on one
// machine, 262 messages, ZERO `.jsonl` files, the oldest from 2025-09.
//
// A chat is ONE JSON document: the session id at the top, the turns in a
// `messages` array. The `$set` cases are gone because no such record exists —
// they were testing a format nobody produces. What replaced them tests the two
// things that really do appear: a non-user turn, and a user turn with no text.
//
// internal/geminichat owns the shape and carries a REAL captured file.
const geminiFixture = `{
  "sessionId": "sess_123",
  "projectHash": "abc123",
  "startTime": "2026-07-21T10:00:00Z",
  "lastUpdated": "2026-07-21T10:05:00Z",
  "messages": [
    {"id":"msg-uuid-001","timestamp":"2026-07-21T10:00:05Z","type":"user",
     "content":[{"text":"hello "},{"text":"world"}]},
    {"id":"msg-uuid-002","timestamp":"2026-07-21T10:00:10Z","type":"gemini",
     "content":"ok"},
    {"id":"msg-uuid-003","timestamp":"2026-07-21T10:00:15Z","type":"user",
     "content":[{"text":"second prompt"}]}
  ],
  "kind": "main"
}`

func writeGeminiChat(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "session-2026-07-21T10-00-sess1234.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeGeminiFixture(t *testing.T) string {
	t.Helper()
	return writeGeminiChat(t, geminiFixture)
}

func TestGeminiReaderSource(t *testing.T) {
	if got := NewGeminiReader().Source(); got != "gemini_cli" {
		t.Fatalf("source=%q, want gemini_cli", got)
	}
}

// THE PRIMARY PATH: the correlation id is Gemini's own OTEL id,
// "<sessionId>########<ordinal>", because Atlas joins it to
// ToolEvent.prompt_id. The record uuid appears in no telemetry.
func TestGeminiReaderReadByOrdinal(t *testing.T) {
	r := NewGeminiReader()
	p := writeGeminiFixture(t)

	if txt, ok := r.Read(p, "sess_123########0"); !ok || txt != "hello world" {
		t.Fatalf("ordinal 0: %q ok=%v, want (hello world,true)", txt, ok)
	}
	if txt, ok := r.Read(p, "sess_123########1"); !ok || txt != "second prompt" {
		t.Fatalf("ordinal 1: %q ok=%v, want (second prompt,true)", txt, ok)
	}
	if _, ok := r.Read(p, "sess_123########9"); ok {
		t.Error("an out-of-range ordinal must not resolve")
	}
	// A malformed ordinal must NOT fall back to the record-uuid path: that
	// would resolve some unrelated prompt rather than nothing.
	if _, ok := r.Read(p, "sess_123########nope"); ok {
		t.Error("a malformed ordinal must not resolve")
	}
}

// The legacy path: pointers spooled before the ordinal scheme carry the record
// uuid, and must still resolve.
func TestGeminiReaderReadByRecordID(t *testing.T) {
	r := NewGeminiReader()
	p := writeGeminiFixture(t)
	if text, ok := r.Read(p, "msg-uuid-001"); !ok || text != "hello world" {
		t.Fatalf("read msg-uuid-001: %q ok=%v, want (hello world,true)", text, ok)
	}
	if text, ok := r.Read(p, "msg-uuid-003"); !ok || text != "second prompt" {
		t.Fatalf("read msg-uuid-003: %q ok=%v, want (second prompt,true)", text, ok)
	}
}

// A model turn is not a prompt, and neither is the session's own id.
func TestGeminiReaderReadsOnlyUserTurns(t *testing.T) {
	r := NewGeminiReader()
	p := writeGeminiFixture(t)
	if _, ok := r.Read(p, "msg-uuid-002"); ok {
		t.Error("a gemini turn must not resolve as a prompt")
	}
	if _, ok := r.Read(p, "sess_123"); ok {
		t.Error("the session id is not a record id")
	}
	if _, ok := r.Read(p, "msg-uuid-999"); ok {
		t.Error("a missing id must return ok=false")
	}
}

// ⚠️ An empty user turn must not CONSUME AN ORDINAL, or every prompt after it
// resolves to the text of the one before.
func TestGeminiReaderEmptyContentTakesNoOrdinal(t *testing.T) {
	p := writeGeminiChat(t, `{
      "sessionId":"sess_123",
      "messages":[
        {"id":"msg-uuid-empty","type":"user","content":[]},
        {"id":"msg-uuid-blank","type":"user","content":"   "},
        {"id":"msg-uuid-valid","type":"user","content":[{"text":"hello"}]}
      ]}`)
	r := NewGeminiReader()
	if _, ok := r.Read(p, "msg-uuid-empty"); ok {
		t.Error("empty content must return ok=false")
	}
	if text, ok := r.Read(p, "msg-uuid-valid"); !ok || text != "hello" {
		t.Fatalf("msg-uuid-valid: %q ok=%v, want (hello,true)", text, ok)
	}
	if text, ok := r.Read(p, "sess_123########0"); !ok || text != "hello" {
		t.Fatalf("ordinal 0 must be the first GENUINE prompt: %q ok=%v", text, ok)
	}
}

func TestGeminiReaderRecentUserPrompts(t *testing.T) {
	r := NewGeminiReader()
	got := r.RecentUserPrompts(writeGeminiFixture(t), "msg-uuid-003", 5)
	if len(got) != 1 || got[0] != "hello world" {
		t.Fatalf("recent (excluding current): %v, want [hello world]", got)
	}
	// And by ordinal, which is how a live pointer names the current prompt.
	got = r.RecentUserPrompts(writeGeminiFixture(t), "sess_123########1", 5)
	if len(got) != 1 || got[0] != "hello world" {
		t.Fatalf("recent by ordinal: %v, want [hello world]", got)
	}
}

func TestGeminiReaderRecentUserPromptsNewestFirst(t *testing.T) {
	p := writeGeminiChat(t, `{
      "sessionId":"sess_123",
      "messages":[
        {"id":"msg-uuid-001","type":"user","content":[{"text":"first"}]},
        {"id":"msg-uuid-002","type":"user","content":[{"text":"second"}]},
        {"id":"msg-uuid-003","type":"user","content":[{"text":"third"}]}
      ]}`)
	got := NewGeminiReader().RecentUserPrompts(p, "msg-uuid-003", 5)
	if len(got) != 2 || got[0] != "second" || got[1] != "first" {
		t.Fatalf("recent newest-first: %v, want [second first]", got)
	}
}

// A truncated or half-written document is unreadable AS A WHOLE — there is no
// "valid prefix" of a JSON document the way there is of a JSONL file. The
// reader must say so rather than answer from nothing; the next poll reads the
// whole file. (A chat file is rewritten whole by Gemini on each turn.)
func TestGeminiReaderRefusesAMalformedDocument(t *testing.T) {
	p := writeGeminiChat(t, `{"sessionId":"sess_123","messages":[{"id":"a",`)
	if _, ok := NewGeminiReader().Read(p, "sess_123########0"); ok {
		t.Error("a half-written document must not resolve")
	}
	if got := NewGeminiReader().RecentUserPrompts(p, "sess_123########0", 5); got != nil {
		t.Errorf("a half-written document must yield no recent prompts, got %v", got)
	}
}

func TestResolveGeminiSource(t *testing.T) {
	text, ok := Resolve("gemini_cli", writeGeminiFixture(t), "sess_123########0", "")
	if !ok || text != "hello world" {
		t.Fatalf("resolve gemini: %q ok=%v, want (hello world,true)", text, ok)
	}
}

// Both content shapes occur in real data: a bare string on 258 of 262 measured
// messages, an array of {text} blocks on 4.
func TestGeminiReaderBothContentShapes(t *testing.T) {
	p := writeGeminiChat(t, `{
      "sessionId":"sess_123",
      "messages":[
        {"id":"msg-uuid-string","type":"user","content":"hello world"},
        {"id":"msg-uuid-array","type":"user","content":[{"text":"array form"}]}
      ]}`)
	r := NewGeminiReader()
	if text, ok := r.Read(p, "msg-uuid-string"); !ok || text != "hello world" {
		t.Fatalf("string-form content: %q ok=%v", text, ok)
	}
	if text, ok := r.Read(p, "msg-uuid-array"); !ok || text != "array form" {
		t.Fatalf("array-form content: %q ok=%v", text, ok)
	}
}

// ⚠️ **THE HOOK AND THE WATCHER CALL GEMINI BY DIFFERENT NAMES, AND Resolve
// DISPATCHES ON THAT NAME.** keld writes `keld __hook --source gemini` into
// ~/.gemini/settings.json (tools.GeminiAdapter is Name()d "gemini") while the
// watcher root, this reader and the conformance tool id all say "gemini_cli".
// An unregistered source is a deliberate SKIP, not an error, so every
// hook-delivered Gemini prompt resolved no text and published nothing, silently.
// Measured: `transcript` PASS with 2 prompt ids read, `publish` 0, hook verified
// to fire.
func TestBothGeminiSourceNamesResolve(t *testing.T) {
	p := writeGeminiFixture(t)
	for _, src := range []string{"gemini_cli", "gemini"} {
		text, ok := Resolve(src, p, "sess_123########0", "")
		if !ok || text != "hello world" {
			t.Errorf("Resolve(%q) = %q ok=%v — a prompt this source delivers "+
				"cannot be enriched at all", src, text, ok)
		}
	}
}
