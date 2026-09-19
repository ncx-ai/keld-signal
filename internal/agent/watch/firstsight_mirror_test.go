package watch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/spool"
)

// line builds one Claude-Code-shaped user line stamped at `at`.
func stampedLine(id string, at time.Time) string {
	return `{"type":"user","promptId":"` + id + `","uuid":"u-` + id + `","sessionId":"s",` +
		`"timestamp":"` + at.UTC().Format(time.RFC3339Nano) + `",` +
		`"message":{"role":"user","content":"x"},"cwd":"/w","version":"1","gitBranch":"m"}` + "\n"
}

// ⚠️ A SESSION WRITTEN ENTIRELY BETWEEN TWO POLLS WAS NEVER MIRRORED. First
// sighting jumps the cursor to EOF, which is right for the PROMPT path — it is
// what stops a fresh install enriching a machine's whole history — and wrong
// for the usage mirror, which then never sees a line of that session. Every
// `codex exec` and every `claude -p` is exactly that shape: conformance chain A
// for codex published enrichments and blocks and forwarded ZERO usage, while
// the rollout on disk carried two token_count records.
func TestAFreshFirstSightingReachesTheMirrorButNotThePromptPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")

	var offered []spool.Pointer
	var observed int
	w := testWatcher(t, Root{SourceID: "claude_code", Dir: dir},
		func(p spool.Pointer) { offered = append(offered, p) }, false)
	w.observe = func(string, string, []byte) { observed++ }

	// The whole session lands after the watcher started, between two polls.
	body := stampedLine("A", w.started.Add(time.Second)) + stampedLine("B", w.started.Add(2*time.Second))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	w.pollOnce()

	if observed == 0 {
		t.Error("the mirror saw nothing: a session written between two polls reports no usage at all")
	}
	if len(offered) != 0 {
		t.Errorf("%d prompt(s) offered on a first sighting; forward-only is what stops a fresh install "+
			"enriching a machine's whole history", len(offered))
	}
}

// The bound is each LINE'S own instant, not the file's mtime. A session open for
// days has a fresh mtime, so a daemon restart would otherwise re-mirror its
// whole history — double-counted spend, which this codebase calls worse than
// missing spend.
func TestLinesOlderThanTheWatcherAreNotMirroredAgain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "s.jsonl")

	var observed int
	w := testWatcher(t, Root{SourceID: "claude_code", Dir: dir}, func(spool.Pointer) {}, false)
	w.observe = func(string, string, []byte) { observed++ }

	// Written before this watcher existed, but the file is touched now — the
	// shape of a long-running session across a daemon restart.
	body := stampedLine("OLD1", w.started.Add(-48*time.Hour)) + stampedLine("OLD2", w.started.Add(-time.Hour))
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	w.pollOnce()

	if observed != 0 {
		t.Errorf("%d line(s) from before the watcher started were mirrored; that usage was already sent "+
			"by whoever was running then", observed)
	}
}
