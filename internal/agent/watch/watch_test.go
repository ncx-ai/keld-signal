package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/spool"
)

func genuine(id string) string {
	return `{"type":"user","promptId":"` + id + `","cwd":"/w","sessionId":"S1","message":{"role":"user","content":"hi ` + id + `"}}` + "\n"
}

func toolResult(id string) string {
	return `{"type":"user","promptId":"` + id + `","message":{"role":"user","content":[{"type":"tool_result","content":"out"}]}}` + "\n"
}

// testWatcher wires a Watcher to a single fixed root + a temp cursor store.
func testWatcher(t *testing.T, root Root, offer func(spool.Pointer), backfill bool) *Watcher {
	t.Helper()
	return &Watcher{
		offer:    offer,
		cursors:  newCursorStoreAt(filepath.Join(t.TempDir(), "cursors.json")),
		discover: func() []Root { return []Root{root} },
		version:  "test",
		poll:     time.Second,
		backfill: backfill,
	}
}

func TestWatcherObserveSeesEveryLine(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	content := genuine("A") +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"hi"}]}}` + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var offered []spool.Pointer
	var observed int
	w := &Watcher{
		offer:    func(p spool.Pointer) { offered = append(offered, p) },
		observe:  func(source, p string, line []byte) { observed++ },
		cursors:  newCursorStoreAt(filepath.Join(t.TempDir(), "c.json")),
		discover: func() []Root { return []Root{{SourceID: "cowork", Dir: dir}} },
		version:  "t", poll: time.Second, backfill: true,
	}
	w.pollOnce()
	if observed != 2 {
		t.Fatalf("observe should see BOTH lines (user + assistant), got %d", observed)
	}
	if len(offered) != 1 {
		t.Fatalf("offer only the genuine prompt, got %d", len(offered))
	}
}

func TestWatcherForwardOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(path, []byte(genuine("OLD")), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []spool.Pointer
	w := testWatcher(t, Root{SourceID: "claude_code", Dir: dir}, func(p spool.Pointer) { got = append(got, p) }, false)

	w.pollOnce() // first sighting: forward-only skips existing content
	if len(got) != 0 {
		t.Fatalf("forward-only should skip pre-existing prompts; got %d", len(got))
	}
	// Append a new prompt.
	appendFile(t, path, genuine("NEW"))
	w.pollOnce()
	if len(got) != 1 {
		t.Fatalf("expected 1 new prompt; got %d", len(got))
	}
	p := got[0]
	if p.Source.ID != "claude_code" || p.Source.Origin != "watch" || p.Correlation.ID != "NEW" ||
		p.Pointer == nil || p.Pointer.PromptID != "NEW" || p.Pointer.Cwd != "/w" || p.Pointer.TranscriptPath != path {
		t.Fatalf("unexpected pointer: %+v", p)
	}
}

func TestWatcherBackfillAndToolResultSkip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	content := genuine("A") + toolResult("T") + genuine("B")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []spool.Pointer
	w := testWatcher(t, Root{SourceID: "cowork", Dir: dir}, func(p spool.Pointer) { got = append(got, p) }, true)
	w.pollOnce()
	if len(got) != 2 {
		t.Fatalf("backfill should offer 2 genuine prompts (tool result skipped); got %d", len(got))
	}
	if got[0].Correlation.ID != "A" || got[1].Correlation.ID != "B" || got[0].Source.ID != "cowork" {
		t.Fatalf("unexpected pointers: %+v", got)
	}
}

func TestWatcherCursorSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(path, []byte(genuine("A")), 0o600); err != nil {
		t.Fatal(err)
	}
	cursorPath := filepath.Join(t.TempDir(), "cursors.json")

	var got []spool.Pointer
	w1 := &Watcher{
		offer: func(p spool.Pointer) { got = append(got, p) }, cursors: newCursorStoreAt(cursorPath),
		discover: func() []Root { return []Root{{SourceID: "cowork", Dir: dir}} }, version: "t", poll: time.Second, backfill: true,
	}
	w1.pollOnce()
	if len(got) != 1 {
		t.Fatalf("first run should offer 1; got %d", len(got))
	}
	// Simulate restart: fresh watcher, same cursor file, no new appends.
	got = nil
	w2 := &Watcher{
		offer: func(p spool.Pointer) { got = append(got, p) }, cursors: newCursorStoreAt(cursorPath),
		discover: func() []Root { return []Root{{SourceID: "cowork", Dir: dir}} }, version: "t", poll: time.Second, backfill: true,
	}
	w2.pollOnce()
	if len(got) != 0 {
		t.Fatalf("restart must not reprocess; got %d", len(got))
	}
}

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}
