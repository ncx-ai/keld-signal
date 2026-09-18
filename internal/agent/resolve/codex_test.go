package resolve

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// codexFixtures are the captured rollouts, shared with the watcher's tests so
// both halves of the seam are measured against the same real files. See
// internal/agent/watch/testdata/codex/PROVENANCE.md.
const codexFixtures = "../watch/testdata/codex"

func codexFixture(name string) string { return filepath.Join(codexFixtures, name) }

// codexTurnsInFixture reads a fixture independently of the reader and returns
// each human turn's `<session>#<turn_id>` and its text.
func codexTurnsInFixture(t *testing.T, name string) (ids, texts []string) {
	t.Helper()
	f, err := os.Open(codexFixture(name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<26)
	var session, pending string
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatal(err)
		}
		p, _ := line["payload"].(map[string]any)
		switch lt, _ := line["type"].(string); lt {
		case "session_meta":
			session, _ = p["id"].(string)
		case "turn_context":
			pending, _ = p["turn_id"].(string)
		case "event_msg":
			pt, _ := p["type"].(string)
			turn, _ := p["turn_id"].(string)
			if turn == "" {
				turn = pending
			}
			switch pt {
			case "user_message":
				msg, _ := p["message"].(string)
				if msg == "" {
					continue
				}
				ids = append(ids, session+"#"+turn)
				texts = append(texts, msg)
			case "item_completed":
				item, _ := p["item"].(map[string]any)
				if it, _ := item["type"].(string); it != "UserMessage" {
					continue
				}
				var parts []string
				content, _ := item["content"].([]any)
				for _, c := range content {
					cm, _ := c.(map[string]any)
					if s, _ := cm["text"].(string); s != "" {
						parts = append(parts, s)
					}
				}
				if len(parts) == 0 {
					continue
				}
				ids = append(ids, session+"#"+turn)
				texts = append(texts, strings.Join(parts, "\n"))
			}
		}
	}
	return ids, texts
}

// TestCodexReaderReadByTurnID is TR-AC-5: Read returns the human text of the
// named turn and no other turn's, over every captured release — including
// 0.151, whose human turns exist ONLY as `item_completed` `UserMessage` items.
func TestCodexReaderReadByTurnID(t *testing.T) {
	r := NewCodexReader()
	if r.Source() != "codex" {
		t.Fatalf("source=%q", r.Source())
	}
	for _, name := range []string{"rollout-0.125.jsonl", "rollout-0.151.jsonl", "rollout-0.153.4.jsonl"} {
		ids, texts := codexTurnsInFixture(t, name)
		if len(ids) == 0 {
			t.Fatalf("%s: oracle found no turns", name)
		}
		for i, id := range ids {
			got, ok := r.Read(codexFixture(name), id)
			if !ok {
				t.Errorf("%s: %s did not resolve", name, id)
				continue
			}
			if got != texts[i] {
				t.Errorf("%s: %s resolved to the wrong turn\n got %.80q\nwant %.80q", name, id, got, texts[i])
			}
			// And to no OTHER turn's text.
			for j, other := range texts {
				if j != i && got == other && texts[i] != other {
					t.Errorf("%s: %s returned turn %d's text", name, id, j)
				}
			}
		}
	}
}

// TestCodexReaderRejectsAnUnknownTurn: a turn id the file does not carry must
// answer not-found rather than the nearest thing.
func TestCodexReaderRejectsAnUnknownTurn(t *testing.T) {
	r := NewCodexReader()
	if text, ok := r.Read(codexFixture("rollout-0.153.4.jsonl"), "thread_1#no-such-turn"); ok {
		t.Fatalf("resolved an unknown turn to %.60q", text)
	}
	if _, ok := r.Read(codexFixture("rollout-0.153.4.jsonl"), "no-separator"); ok {
		t.Fatal("resolved an id with no '#'")
	}
	if _, ok := r.Read(codexFixture("rollout-0.153.4.jsonl"), "thread_1#"); ok {
		t.Fatal("resolved an id with an empty turn key")
	}
}

// TestCodexReaderResolvesTheTimestampFallback: a prompt with no preceding
// turn_context is named `<session>#<timestamp>` by the watcher, so the reader
// must resolve that form too — otherwise the 7.9% of turns that take the
// fallback would be captured and then fail to resolve, which is a partial
// publish rather than a missing one.
func TestCodexReaderResolvesTheTimestampFallback(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	body := `{"timestamp":"2026-09-15T10:00:00.000Z","type":"session_meta","payload":{"id":"thread_1","cwd":"/work"}}
{"timestamp":"2026-09-15T10:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":"no turn context here"}}
{"timestamp":"2026-09-15T10:00:02.000Z","type":"turn_context","payload":{"turn_id":"turn_two","cwd":"/work"}}
{"timestamp":"2026-09-15T10:00:03.000Z","type":"event_msg","payload":{"type":"user_message","message":"second turn"}}
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewCodexReader()
	if got, ok := r.Read(p, "thread_1#2026-09-15T10:00:01.000Z"); !ok || got != "no turn context here" {
		t.Errorf("timestamp fallback: %q ok=%v", got, ok)
	}
	if got, ok := r.Read(p, "thread_1#turn_two"); !ok || got != "second turn" {
		t.Errorf("turn id: %q ok=%v", got, ok)
	}
}

// TestCodexReaderRecentUserPrompts: newest-first, excluding the current turn.
// The window is the last recentTailBytes of the file, so a big rollout yields
// only the turns inside it — the count below is over a small synthetic file so
// the assertion is about ORDER and EXCLUSION rather than about the window.
func TestCodexReaderRecentUserPrompts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout.jsonl")
	body := `{"timestamp":"2026-09-15T10:00:00.000Z","type":"session_meta","payload":{"id":"thread_1","cwd":"/work"}}
{"timestamp":"2026-09-15T10:00:01.000Z","type":"turn_context","payload":{"turn_id":"t1","cwd":"/work"}}
{"timestamp":"2026-09-15T10:00:02.000Z","type":"event_msg","payload":{"type":"user_message","message":"first"}}
{"timestamp":"2026-09-15T10:00:03.000Z","type":"turn_context","payload":{"turn_id":"t2","cwd":"/work"}}
{"timestamp":"2026-09-15T10:00:04.000Z","type":"event_msg","payload":{"type":"user_message","message":"second"}}
{"timestamp":"2026-09-15T10:00:05.000Z","type":"turn_context","payload":{"turn_id":"t3","cwd":"/work"}}
{"timestamp":"2026-09-15T10:00:06.000Z","type":"event_msg","payload":{"type":"user_message","message":"third"}}
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := NewCodexReader().RecentUserPrompts(p, "thread_1#t3", 10)
	want := []string{"second", "first"}
	if len(got) != len(want) {
		t.Fatalf("recent = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("recent = %v, want %v (newest first, current excluded)", got, want)
		}
	}

	// And over the real rollout it reads whatever the tail window holds,
	// newest-first, never including the current turn.
	ids, texts := codexTurnsInFixture(t, "rollout-0.153.4.jsonl")
	last := len(ids) - 1
	real := NewCodexReader().RecentUserPrompts(codexFixture("rollout-0.153.4.jsonl"), ids[last], 10)
	if len(real) == 0 {
		t.Fatal("no recent prompts read from the real rollout")
	}
	for _, s := range real {
		if s == texts[last] {
			t.Error("the current turn was not excluded")
		}
	}
}

// TestCodexReaderRecentUserPromptsReadsTheItemShape: on 0.148-0.151 the recent
// tail is item-shaped too, so a reader that knows only `user_message` returns
// an empty history there and every prompt loses its context.
func TestCodexReaderRecentUserPromptsReadsTheItemShape(t *testing.T) {
	ids, _ := codexTurnsInFixture(t, "rollout-0.151.jsonl")
	if len(ids) < 2 {
		t.Fatalf("fixture has %d turns, need >= 2", len(ids))
	}
	got := NewCodexReader().RecentUserPrompts(codexFixture("rollout-0.151.jsonl"), ids[len(ids)-1], 10)
	if len(got) == 0 {
		t.Fatal("no recent prompts read from the item shape")
	}
}

// TestResolveCodexSource: the reader is reachable through the registry under
// the source id the watcher and the hook both use.
func TestResolveCodexSource(t *testing.T) {
	ids, texts := codexTurnsInFixture(t, "rollout-0.125.jsonl")
	text, ok := Resolve("codex", codexFixture("rollout-0.125.jsonl"), ids[0], "")
	if !ok || text != texts[0] {
		t.Fatalf("resolve codex: %.60q ok=%v", text, ok)
	}
}

// TestCodexReaderMalformedLineIgnored: a garbage line must neither panic nor
// block resolution of a valid line elsewhere in the file.
func TestCodexReaderMalformedLineIgnored(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout-garbage.jsonl")
	body := `{"timestamp":"2026-09-15T10:00:00.000Z","type":"session_meta","payload":{"id":"thread_1","cwd":"/work"}}
not even json {{{
{"timestamp":"2026-09-15T10:00:01.000Z","type":"turn_context","payload":{"turn_id":"t1","cwd":"/work"}}
{"timestamp":"2026-09-15T10:00:02.000Z","type":"event_msg","payload":{"type":"user_message","message":"refactor the auth module"}}
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := NewCodexReader().Read(p, "thread_1#t1"); !ok || got != "refactor the auth module" {
		t.Fatalf("read past a garbage line: %q ok=%v", got, ok)
	}
}

// TestCodexReaderEmptyMessageNotFound: an empty user_message is "not found",
// matching the Claude reader's extractText semantics.
func TestCodexReaderEmptyMessageNotFound(t *testing.T) {
	p := filepath.Join(t.TempDir(), "rollout-empty.jsonl")
	body := `{"timestamp":"2026-09-15T10:00:00.000Z","type":"turn_context","payload":{"turn_id":"t0"}}
{"timestamp":"2026-09-15T10:00:01.000Z","type":"event_msg","payload":{"type":"user_message","message":""}}
{"timestamp":"2026-09-15T10:00:02.000Z","type":"turn_context","payload":{"turn_id":"t1"}}
{"timestamp":"2026-09-15T10:00:03.000Z","type":"event_msg","payload":{"type":"user_message","message":"hello"}}
`
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	r := NewCodexReader()
	if text, ok := r.Read(p, "thread_1#t0"); ok {
		t.Fatalf("empty message resolved to %q", text)
	}
	if got := r.RecentUserPrompts(p, "thread_1#none", 5); len(got) != 1 || got[0] != "hello" {
		t.Fatalf("recent (empty message skipped) = %v", got)
	}
}
