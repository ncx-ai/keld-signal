package blocks

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTheCursorIsPersistedAtomicallyAndReadBack(t *testing.T) {
	file := filepath.Join(t.TempDir(), "nested", "blocks.json")
	s := newState(file)
	now := time.Unix(1787145300, 0)
	s.note("claude_code", "sess", "/p/a.jsonl", now)
	s.advance("/p/a.jsonl", 1787144000)
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	// User-only: a cursor names transcripts on this machine.
	st, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
	// No temp file left behind.
	if _, err := os.Stat(file + ".tmp"); err == nil {
		t.Error("the temp file survived the rename")
	}

	back := newState(file)
	got := back.targets([]string{"/p/a.jsonl"})
	if len(got) != 1 || got[0].Cursor == nil || *got[0].Cursor != 1787144000 {
		t.Fatalf("read back %+v", got)
	}
	if got[0].Source != "claude_code" || got[0].Session != "sess" {
		t.Errorf("read back %+v", got[0])
	}
}

// A lower cursor is a rolled-back store or a late response, never an
// instruction to re-offer settled ground.
func TestTheCursorIsMonotonic(t *testing.T) {
	s := newState(filepath.Join(t.TempDir(), "b.json"))
	s.note("claude_code", "sess", "/p/a.jsonl", time.Unix(100, 0))
	s.advance("/p/a.jsonl", 500)
	s.advance("/p/a.jsonl", 400)
	if got := *s.targets([]string{"/p/a.jsonl"})[0].Cursor; got != 500 {
		t.Fatalf("cursor = %v, want it held at 500", got)
	}
}

// A corrupt file must not fail a daemon start. The cost is stated where it is
// paid: every transcript becomes first-sight and resumes forward-only.
func TestACorruptStateFileStartsFresh(t *testing.T) {
	file := filepath.Join(t.TempDir(), "b.json")
	if err := os.WriteFile(file, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := newState(file).targets([]string{"/p/a.jsonl"}); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

// The prune bounds the file, and the ACTIVE exception is what makes it
// lossless: a transcript with an unpublished backlog never leaves the active
// set, so it can never be pruned out from under its own cursor.
func TestThePruneSparesActiveEntries(t *testing.T) {
	s := newState(filepath.Join(t.TempDir(), "b.json"))
	old := time.Unix(1000000, 0)
	s.note("claude_code", "s1", "/p/stale.jsonl", old)
	s.note("claude_code", "s2", "/p/busy.jsonl", old)
	s.note("claude_code", "s3", "/p/recent.jsonl", old.Add(cursorRetain))

	s.prune(map[string]bool{"/p/busy.jsonl": true}, old.Add(cursorRetain+time.Hour))

	got := s.targets([]string{"/p/stale.jsonl", "/p/busy.jsonl", "/p/recent.jsonl"})
	have := map[string]bool{}
	for _, g := range got {
		have[g.Path] = true
	}
	if have["/p/stale.jsonl"] {
		t.Error("an idle, inactive entry survived the prune — the file grows one entry " +
			"per session forever")
	}
	if !have["/p/busy.jsonl"] {
		t.Error("an ACTIVE entry was pruned — its cursor is what a pending backlog " +
			"resumes from")
	}
	if !have["/p/recent.jsonl"] {
		t.Error("a recently-advanced entry was pruned")
	}
}

// A quiet machine must not rewrite the file every interval.
func TestSaveIsANoOpWhenNothingChanged(t *testing.T) {
	file := filepath.Join(t.TempDir(), "b.json")
	s := newState(file)
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("an unchanged state wrote a file")
	}
	s.note("claude_code", "s", "/p/a.jsonl", time.Unix(1, 0))
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(file, before.ModTime().Add(-time.Hour), before.ModTime().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	stamped, _ := os.Stat(file)
	if err := s.save(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(file)
	if !after.ModTime().Equal(stamped.ModTime()) {
		t.Error("an unchanged state rewrote the file")
	}
}
