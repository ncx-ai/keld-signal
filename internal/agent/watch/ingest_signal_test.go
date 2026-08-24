package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/spool"
)

// The distinctive string every transcript line in this file says. It exists so
// "the signal carries no content" can be asserted against something that would
// actually be visible if it leaked, rather than against the shape of a
// signature that could later grow a third argument.
const saidMarker = "PROMPT-BODY-MUST-NOT-TRAVEL"

func genuineSaying(id, said string) string {
	return `{"type":"user","promptId":"` + id + `","cwd":"/w","sessionId":"S1","message":{"role":"user","content":"` + said + `"}}` + "\n"
}

// advance is one recorded ingest signal: the coordinates, and nothing else.
type advance struct{ source, path string }

// signalWatcher is testWatcher plus a recording ingest signal.
func signalWatcher(t *testing.T, root Root, backfill bool, rec *[]advance) *Watcher {
	t.Helper()
	w := testWatcher(t, root, func(spool.Pointer) {}, backfill)
	w.advanced = func(source, path string) { *rec = append(*rec, advance{source, path}) }
	return w
}

// The signal is per FILE per POLL, not per line and not per prompt. A transcript
// under active use grows on every poll; one signal per appended line would turn
// a 200-line batch into 200 whole-tail ingests of the same file.
func TestIngestSignalFiresOncePerAdvancedFilePerBatch(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "aaaaaaaa-0000.jsonl")
	b := filepath.Join(dir, "bbbbbbbb-0000.jsonl")
	// Three lines each, two of them genuine prompts: neither count may reach the signal.
	content := genuineSaying("A1", saidMarker) +
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"` + saidMarker + `"}]}}` + "\n" +
		genuineSaying("A2", saidMarker)
	for _, p := range []string{a, b} {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var got []advance
	w := signalWatcher(t, Root{SourceID: "claude_code", Dir: dir}, true, &got)

	w.pollOnce()
	if len(got) != 2 {
		t.Fatalf("want exactly one signal per advanced file (2 files), got %d: %+v", len(got), got)
	}
	seen := map[string]int{}
	for _, g := range got {
		seen[g.path]++
		if g.source != "claude_code" {
			t.Errorf("signal source = %q, want claude_code", g.source)
		}
	}
	if seen[a] != 1 || seen[b] != 1 {
		t.Fatalf("each file must be signalled exactly once: %+v", seen)
	}
}

// Coordinates only. A path is what the sidecar needs to resume from its own byte
// offset; the bytes themselves stay on this side of the call.
func TestIngestSignalCarriesNoPromptContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cccccccc-0000.jsonl")
	if err := os.WriteFile(path, []byte(genuineSaying("C1", saidMarker)), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []advance
	w := signalWatcher(t, Root{SourceID: "cowork", Dir: dir}, true, &got)
	w.pollOnce()

	if len(got) != 1 {
		t.Fatalf("want 1 signal, got %d", len(got))
	}
	if got[0].path != path {
		t.Errorf("signal path = %q, want %q", got[0].path, path)
	}
	if joined := got[0].source + "\x00" + got[0].path; strings.Contains(joined, saidMarker) {
		t.Errorf("the signal carried prompt content: %q", joined)
	}
}

// The startup case, and the reason a daemon restart is not a thundering herd of
// whole-file ingests: forward-only (KELD_WATCH_BACKFILL off, the default) sets a
// new file's cursor to EOF and consumes nothing, so nothing advanced. Only a
// file that grows AFTER the daemon came up is signalled.
func TestForwardOnlyFirstSightingDoesNotSignal(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dddddddd-0000.jsonl")
	if err := os.WriteFile(path, []byte(genuineSaying("D1", saidMarker)), 0o600); err != nil {
		t.Fatal(err)
	}
	var got []advance
	w := signalWatcher(t, Root{SourceID: "claude_code", Dir: dir}, false, &got)

	w.pollOnce() // first sighting: cursor jumps to EOF, no bytes consumed
	if len(got) != 0 {
		t.Fatalf("a pre-existing file must not be signalled forward-only: %+v", got)
	}
	w.pollOnce() // nothing appended
	if len(got) != 0 {
		t.Fatalf("an unchanged file must not be signalled: %+v", got)
	}

	appendFile(t, path, genuineSaying("D2", saidMarker))
	w.pollOnce()
	if len(got) != 1 || got[0].path != path {
		t.Fatalf("a grown file must be signalled exactly once: %+v", got)
	}
	w.pollOnce() // and not again
	if len(got) != 1 {
		t.Fatalf("no further signal without further growth: %+v", got)
	}
}

// A watcher with no signal wired (every existing construction, and every test
// above this task) must behave exactly as before.
func TestNilIngestSignalIsInert(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "eeeeeeee-0000.jsonl")
	if err := os.WriteFile(path, []byte(genuineSaying("E1", saidMarker)), 0o600); err != nil {
		t.Fatal(err)
	}
	var offered []spool.Pointer
	w := testWatcher(t, Root{SourceID: "claude_code", Dir: dir}, func(p spool.Pointer) { offered = append(offered, p) }, true)
	w.pollOnce() // must not panic through a nil hook
	if len(offered) != 1 {
		t.Fatalf("capture unaffected by an absent ingest signal, got %d", len(offered))
	}
}

// WithIngestSignal is the wiring seam the daemon uses; it must compose with New
// rather than requiring a sixth positional argument at every construction.
func TestWithIngestSignalWires(t *testing.T) {
	var got []advance
	w := New(func(spool.Pointer) {}, nil, "t", time.Second, false).
		WithIngestSignal(func(source, path string) { got = append(got, advance{source, path}) })
	if w.advanced == nil {
		t.Fatal("WithIngestSignal did not install the hook")
	}
	w.advanced("claude_code", "/x.jsonl")
	if len(got) != 1 || got[0].source != "claude_code" || got[0].path != "/x.jsonl" {
		t.Fatalf("hook not called through: %+v", got)
	}
}
