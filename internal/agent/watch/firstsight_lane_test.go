package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/spool"
)

// ⚠️ THE WATCHER LANE WAS WRITTEN ONLY ON THE OFFER, AND A FIRST SIGHTING
// OFFERS NOTHING. The cursor jumps to EOF — right for the prompt path, which is
// what stops a fresh install enriching a machine's whole history — so a session
// created, written and finished between two 5-second polls recorded no lane at
// all, while this same branch replayed the file to the usage mirror. Every
// `codex exec` and `claude -p` has that shape, as does any session whose first
// prompt lands inside the poll gap.
//
// Silence on an expected lane is one half of `broken`, so the pane reported
// `broken · watcher` about a prompt the watcher had just read. Measured on a
// real machine: a live Codex session with its usage mirrored and its blocks cut
// and delivered, badged broken throughout.
func TestAFreshFirstSightingRecordsTheWatcherLaneWithNothingOffered(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")

	var offered []spool.Pointer
	var lanes []string
	w := testWatcher(t, Root{SourceID: "claude_code", Dir: dir},
		func(p spool.Pointer) { offered = append(offered, p) }, false)
	w.notePrompt = func(source, _ string) { lanes = append(lanes, source) }

	body := stampedLine("A", w.started.Add(time.Second)) + stampedLine("B", w.started.Add(2*time.Second))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	w.pollOnce()

	if len(offered) != 0 {
		t.Fatalf("%d prompt(s) offered on a first sighting; the gap this test covers is the branch that offers none", len(offered))
	}
	if len(lanes) != 2 {
		t.Fatalf("the watcher lane recorded %d prompt(s), want 2; the pane reads `broken · watcher` "+
			"on a machine whose watcher just read both", len(lanes))
	}
	for _, s := range lanes {
		if s != "claude_code" {
			t.Errorf("lane recorded for source %q, want claude_code", s)
		}
	}
}

// The freshness bound is the LINE'S OWN instant, the same rule the mirror
// obeys. A lane vouched for by a prompt written before this watcher started is
// reporting on somebody else's history — and on a machine whose transcripts all
// predate the daemon it would report a lane that has carried nothing since.
func TestPromptsOlderThanTheWatcherDoNotVouchForTheLane(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")

	var lanes int
	w := testWatcher(t, Root{SourceID: "claude_code", Dir: dir}, func(spool.Pointer) {}, false)
	w.notePrompt = func(string, string) { lanes++ }

	body := stampedLine("old", w.started.Add(-time.Hour))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	// Keep the file itself fresh, so only the line's own instant can refuse it.
	now := time.Now()
	if err := os.Chtimes(path, now, now); err != nil {
		t.Fatal(err)
	}
	w.pollOnce()

	if lanes != 0 {
		t.Errorf("a prompt written before the watcher started recorded %d lane fact(s); history must not vouch for a live lane", lanes)
	}
}

// The steady-state tail path records too, so the two branches cannot disagree
// about one machine: a transcript that grows after first sight is the case the
// offer already covered, and it must keep reporting the same fact.
func TestAppendedPromptsRecordTheLaneToo(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	var lanes int
	w := testWatcher(t, Root{SourceID: "claude_code", Dir: dir}, func(spool.Pointer) {}, false)
	w.notePrompt = func(string, string) { lanes++ }
	w.pollOnce() // first sight of an empty file: nothing to record

	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(stampedLine("C", time.Now())); err != nil {
		t.Fatal(err)
	}
	f.Close()
	w.pollOnce()

	if lanes != 1 {
		t.Errorf("appended prompt recorded %d lane fact(s), want 1", lanes)
	}
}
