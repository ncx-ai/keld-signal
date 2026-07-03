package resolve

import (
	"os"
	"path/filepath"
	"testing"
)

// writeRecentTranscript writes a JSONL transcript from lines (one JSON object per
// line, newline appended). Named distinctly from claude_test.go's writeTranscript
// (which takes a raw body string) to avoid redeclaration in this package.
func writeRecentTranscript(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(p, []byte(joinRecentLines(lines)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func joinRecentLines(ls []string) string {
	out := ""
	for _, l := range ls {
		out += l + "\n"
	}
	return out
}

func TestRecentPromptsNewestFirstExcludingCurrent(t *testing.T) {
	p := writeRecentTranscript(t, []string{
		`{"type":"user","promptId":"p1","message":{"role":"user","content":"first task"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"ok"}}`,
		`{"type":"user","promptId":"p2","message":{"role":"user","content":"second task"}}`,
		`{"type":"user","promptId":"p3","message":{"role":"user","content":"ok that's fine"}}`,
	})
	got := RecentPrompts("claude_code", p, "p3", 3)
	want := []string{"second task", "first task"} // newest-first, current (p3) excluded
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("index %d: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestRecentPromptsRespectsN(t *testing.T) {
	p := writeRecentTranscript(t, []string{
		`{"type":"user","promptId":"a","message":{"role":"user","content":"one"}}`,
		`{"type":"user","promptId":"b","message":{"role":"user","content":"two"}}`,
		`{"type":"user","promptId":"c","message":{"role":"user","content":"three"}}`,
	})
	if got := RecentPrompts("claude_code", p, "c", 1); len(got) != 1 || got[0] != "two" {
		t.Fatalf("N=1 got %v", got)
	}
}

func TestRecentPromptsUnsupportedSourceNil(t *testing.T) {
	p := writeRecentTranscript(t, []string{`{"type":"user","promptId":"x","message":{"role":"user","content":"hi"}}`})
	if got := RecentPrompts("codex", p, "y", 3); got != nil {
		t.Fatalf("unsupported source should be nil, got %v", got)
	}
	if got := RecentPrompts("claude_code", "", "y", 3); got != nil {
		t.Fatalf("empty path should be nil, got %v", got)
	}
}
