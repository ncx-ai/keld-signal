package resolve

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The transcript every ids test reads. Text is planted DISTINCTIVELY so the
// no-text assertion below can be exact rather than shape-based.
var idsTranscript = []string{
	`{"type":"user","uuid":"u1","promptId":"p1","message":{"role":"user","content":"the first distinctive task"}}`,
	`{"type":"assistant","uuid":"a1","promptId":"p1","message":{"role":"assistant","content":"working on it"}}`,
	// A tool result: type "user", but it is agent output, not a human prompt.
	`{"type":"user","uuid":"u2","promptId":"p1","toolUseResult":{"ok":true},"message":{"role":"user","content":[{"type":"tool_result","content":"file contents"}]}}`,
	`{"type":"user","uuid":"u3","promptId":"p2","message":{"role":"user","content":"the second distinctive task"}}`,
	// An injected/caveat record: real text, but the hook never fires for it.
	`{"type":"user","uuid":"u4","promptId":"p9","isMeta":true,"message":{"role":"user","content":"Caveat: this message was injected"}}`,
	// A subagent turn: another transcript's work, in this file.
	`{"type":"user","uuid":"u5","promptId":"p8","isSidechain":true,"message":{"role":"user","content":"subagent instructions"}}`,
	`{"type":"user","uuid":"u6","promptId":"p3","message":{"role":"user","content":"the third distinctive task"}}`,
}

func TestRecentPromptIDsReturnsIDsAndNeverPromptText(t *testing.T) {
	p := writeRecentTranscript(t, idsTranscript)
	got := RecentPromptIDs("claude_code", p, "p3", 10)
	want := []string{"p2", "p1"} // newest-first, current (p3) excluded
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q want %q", i, got[i], want[i])
		}
	}
	// THE INVARIANT THIS FUNCTION EXISTS FOR. Its sibling RecentPrompts returns
	// prompt TEXT to fill a model's context window on this device; these ids ride
	// an /analyze request to the sidecar, where text must never go. Asserted
	// against the transcript's own text rather than against a shape, so a future
	// refactor that reaches for `up.text` by mistake fails here.
	texts := RecentPrompts("claude_code", p, "p3", 10)
	if len(texts) == 0 {
		t.Fatal("premise: the fixture must have prompt text for this to assert anything")
	}
	for _, id := range got {
		for _, txt := range texts {
			if id == txt {
				t.Fatalf("RecentPromptIDs returned prompt TEXT: %q", id)
			}
		}
		if strings.Contains(id, " ") || strings.Contains(id, "distinctive") {
			t.Fatalf("RecentPromptIDs returned something that is not an id: %q", id)
		}
	}
}

func TestRecentPromptIDsAppliesTheWatchersHumanPromptFilter(t *testing.T) {
	// The three non-human records in the fixture (a tool result, an isMeta
	// caveat, an isSidechain subagent turn) must not become episodes: they are in
	// the sidecar's turn index, so an id for one WOULD resolve to an instant and
	// silently make the covers mapping a mapping over turns.
	p := writeRecentTranscript(t, idsTranscript)
	got := RecentPromptIDs("claude_code", p, "", 10)
	for _, id := range got {
		if id == "p8" || id == "p9" || id == "" {
			t.Fatalf("a non-human record reached the ids: %v", got)
		}
	}
	if len(got) != 3 { // p3, p2, p1 — the human prompts, current excluded by nothing here
		t.Fatalf("got %v, want the three human prompts", got)
	}
}

func TestRecentPromptIDsCollapsesOneIDSpanningSeveralLines(t *testing.T) {
	// One promptId can appear on several transcript lines. A repeat would define
	// a zero-length episode at the same instant and steal the real one's span.
	p := writeRecentTranscript(t, []string{
		`{"type":"user","uuid":"u1","promptId":"p1","message":{"role":"user","content":"part one"}}`,
		`{"type":"user","uuid":"u2","promptId":"p1","message":{"role":"user","content":"part two"}}`,
		`{"type":"user","uuid":"u3","promptId":"p2","message":{"role":"user","content":"next"}}`,
	})
	if got := RecentPromptIDs("claude_code", p, "p2", 10); len(got) != 1 || got[0] != "p1" {
		t.Fatalf("got %v, want one p1", got)
	}
}

func TestRecentPromptIDsRespectsNAndRejectsUnusableInput(t *testing.T) {
	p := writeRecentTranscript(t, idsTranscript)
	if got := RecentPromptIDs("claude_code", p, "p3", 1); len(got) != 1 || got[0] != "p2" {
		t.Fatalf("n=1 got %v", got)
	}
	if got := RecentPromptIDs("claude_code", p, "p3", 0); got != nil {
		t.Fatalf("n=0 got %v, want nil", got)
	}
	if got := RecentPromptIDs("claude_code", "", "p3", 5); got != nil {
		t.Fatalf("no path got %v, want nil", got)
	}
	// A source with no reader at all answers nil rather than guessing a format.
	if got := RecentPromptIDs("nosuchtool", p, "p3", 5); got != nil {
		t.Fatalf("unknown source got %v, want nil", got)
	}
}

// TestRecentPromptIDsCostOnAFullBudgetTail measures what the widened tail costs,
// because idTailBytes is 128x the text scan's window and a per-job cost that big
// must be a number rather than an assumption. The fixture mimics a real
// transcript's SHAPE — a human prompt separated from the next by megabytes of
// agent output, most of it in tool_result lines the pre-filter skips unparsed.
//
// It asserts only a loose ceiling: the point is the logged figure, and a machine
// under load must not fail a build over it.
func TestRecentPromptIDsCostOnAFullBudgetTail(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.jsonl")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	blob := strings.Repeat("x", 64*1024)
	written := 0
	prompts := 0
	for written < idTailBytes+(2<<20) {
		// One human prompt per ~1.3 MB of agent output, the density measured on
		// the 90 MB transcript (75 human prompts).
		line, _ := json.Marshal(map[string]any{
			"type": "user", "uuid": fmt.Sprintf("u%d", prompts),
			"promptId": fmt.Sprintf("p%d", prompts),
			"message":  map[string]any{"role": "user", "content": "do the thing"},
		})
		n, _ := f.Write(append(line, '\n'))
		written += n
		prompts++
		for i := 0; i < 20; i++ {
			tr, _ := json.Marshal(map[string]any{
				"type": "user", "uuid": fmt.Sprintf("t%d-%d", prompts, i),
				"toolUseResult": map[string]any{"ok": true},
				"message": map[string]any{"role": "user",
					"content": []any{map[string]any{"type": "tool_result", "content": blob}}},
			})
			n, _ := f.Write(append(tr, '\n'))
			written += n
		}
	}
	f.Close()

	start := time.Now()
	got := RecentPromptIDs("claude_code", p, "", 250)
	elapsed := time.Since(start)
	t.Logf("scanned a %d MB tail of a %d MB transcript in %s, found %d prompt ids",
		idTailBytes>>20, written>>20, elapsed, len(got))
	if len(got) == 0 {
		t.Fatal("the budget tail yielded no ids at all")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("a full-budget tail scan took %s, which is a per-job cost", elapsed)
	}
}
