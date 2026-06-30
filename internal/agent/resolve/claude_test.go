package resolve

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func userLine(promptID, text string) string {
	return `{"type":"user","promptId":"` + promptID + `","message":{"role":"user","content":"` + text + `"}}` + "\n"
}

const sampleJSONL = `{"type":"summary"}
` + `{"type":"user","promptId":"P1","message":{"role":"user","content":"hello world"}}
` + `{"type":"assistant","message":{"role":"assistant","content":"hi"}}
`

func writeTranscript(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func appendLine(t *testing.T, path, line string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeReaderFindsPromptByID(t *testing.T) {
	p := writeTranscript(t, sampleJSONL)
	r := NewClaudeReader()
	got, ok := r.Read(p, "P1")
	if !ok || got != "hello world" {
		t.Fatalf("got (%q,%v), want (hello world,true)", got, ok)
	}
}

func TestClaudeReaderMissingPromptReturnsFalse(t *testing.T) {
	p := writeTranscript(t, sampleJSONL)
	r := NewClaudeReader()
	r.Attempts, r.Delay = 2, time.Millisecond
	if _, ok := r.Read(p, "NOPE"); ok {
		t.Fatal("missing prompt id must return ok=false")
	}
}

func TestClaudeReaderToleratesMalformedLines(t *testing.T) {
	body := "not json\n" + sampleJSONL + "{bad\n"
	p := writeTranscript(t, body)
	if _, ok := NewClaudeReader().Read(p, "P1"); !ok {
		t.Fatal("malformed lines must be skipped, valid line still found")
	}
}

// After consuming P1, the cursor advances past it: a second read does NOT
// re-scan it (proves incremental, non-O(n) behaviour). A subsequently appended
// P2 is found by reading only the new tail.
func TestClaudeReaderIncrementalAdvancesCursor(t *testing.T) {
	p := writeTranscript(t, userLine("P1", "first"))
	r := NewClaudeReader()
	r.Attempts, r.Delay = 1, time.Millisecond

	if got, ok := r.Read(p, "P1"); !ok || got != "first" {
		t.Fatalf("first read: (%q,%v)", got, ok)
	}
	// P1 is now behind the cursor; re-requesting it must not re-scan from 0.
	if _, ok := r.Read(p, "P1"); ok {
		t.Fatal("P1 should be behind the cursor after first read")
	}
	appendLine(t, p, userLine("P2", "second"))
	if got, ok := r.Read(p, "P2"); !ok || got != "second" {
		t.Fatalf("incremental read of P2: (%q,%v)", got, ok)
	}
}

// A trailing line without a newline (write in flight) must not be consumed; once
// the rest is flushed, the next read finds it.
func TestClaudeReaderPartialLineNotConsumed(t *testing.T) {
	complete := userLine("P1", "done")
	partial := `{"type":"user","promptId":"P2","message":{"role":"user","content":"flush` // no closing + newline
	p := writeTranscript(t, complete+partial)
	r := NewClaudeReader()
	r.Attempts, r.Delay = 1, time.Millisecond

	if _, ok := r.Read(p, "P2"); ok {
		t.Fatal("partial line must not be matched yet")
	}
	// Overwrite with the fully-flushed version; cursor (past P1) still valid.
	if err := os.WriteFile(p, []byte(complete+userLine("P2", "flushed")), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := r.Read(p, "P2"); !ok || got != "flushed" {
		t.Fatalf("after flush: (%q,%v)", got, ok)
	}
}

// If the file shrinks (truncation / rotation / compaction), the cursor resets.
func TestClaudeReaderResetsOnTruncation(t *testing.T) {
	p := writeTranscript(t, userLine("P1", "a")+userLine("P2", "b")+userLine("P3", "c"))
	r := NewClaudeReader()
	r.Attempts, r.Delay = 1, time.Millisecond
	if _, ok := r.Read(p, "P3"); !ok { // advance cursor near EOF
		t.Fatal("expected P3")
	}
	// Replace with a smaller file; cursor (> new size) must reset to 0.
	if err := os.WriteFile(p, []byte(userLine("P9", "z")), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, ok := r.Read(p, "P9"); !ok || got != "z" {
		t.Fatalf("after truncation: (%q,%v)", got, ok)
	}
}

func TestResolveInlineWins(t *testing.T) {
	got, ok := Resolve("claude_desktop", "", "", "inline text")
	if !ok || got != "inline text" {
		t.Fatalf("inline path failed: (%q,%v)", got, ok)
	}
}
