package usage

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ledger"
	"github.com/ncx-ai/keld-signal/internal/agent/watch"
)

// copyFixture puts one real fixture into dir under name.
func copyFixture(t *testing.T, fixtureName, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(fixture(fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// machine lays out one transcript per tool, the way the watcher finds them.
func machine(t *testing.T) []watch.Root {
	t.Helper()
	base := t.TempDir()
	cc := filepath.Join(base, "claude", "projects")
	cx := filepath.Join(base, "codex", "sessions")
	gm := filepath.Join(base, "gemini", "tmp", "p", "chats")
	copyFixture(t, "claude_code_session.jsonl", filepath.Join(cc, "-proj"), "s.jsonl")
	copyFixture(t, "codex_rollout.jsonl", filepath.Join(cx, "2026", "09", "18"), "rollout-a.jsonl")
	copyFixture(t, "gemini_session.json", gm, "session-2026-09-19T10-00-abc.json")
	return []watch.Root{
		{SourceID: "claude_code", Dir: cc},
		{SourceID: "codex", Dir: cx},
		{SourceID: "gemini_cli", Dir: filepath.Join(base, "gemini", "tmp")},
	}
}

func runToEnd(t *testing.T, b *Backfill) int {
	t.Helper()
	steps := 0
	for !b.Step() {
		steps++
		if steps > 1000 {
			t.Fatal("backfill never finished")
		}
	}
	return steps
}

func allRows(t *testing.T, s *ledger.Store) []ledger.RequestRow {
	t.Helper()
	rows, err := s.UsageRows(time.Time{}, time.Now().AddDate(10, 0, 0))
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

// Every transcript on disk is read from its start, once, into the table; the
// marker is set when it finishes and a second run does nothing.
func TestBackfillReadsEveryTranscriptOnceAndMarksDone(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	roots := machine(t)
	b := NewBackfill(store, flatPrice, func() []watch.Root { return roots })
	runToEnd(t, b)
	if got := len(allRows(t, store)); got != 2+4+4 {
		t.Fatalf("backfill wrote %d requests, want 10", got)
	}
	if !store.RequestsBackfillDone() {
		t.Fatal("marker not set after the backfill finished")
	}
	again := NewBackfill(store, flatPrice, func() []watch.Root {
		t.Fatal("a finished backfill listed the disk again")
		return nil
	})
	if !again.Step() {
		t.Fatal("a finished backfill did not report done on its first step")
	}
}

// R3: paced — no more than PerStep files are read in one step.
func TestBackfillIsPaced(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := filepath.Join(t.TempDir(), "projects")
	for i := 0; i < 10; i++ {
		copyFixture(t, "claude_code_session.jsonl", dir, "s"+string(rune('a'+i))+".jsonl")
	}
	b := NewBackfill(ledger.New(), flatPrice, func() []watch.Root {
		return []watch.Root{{SourceID: "claude_code", Dir: dir}}
	})
	b.PerStep = 4
	for _, want := range []int{6, 2} {
		if b.Step() {
			t.Fatal("finished early")
		}
		if got := b.Remaining(); got != want {
			t.Fatalf("after a step %d files remain, want %d", got, want)
		}
	}
	if !b.Step() || b.Remaining() != 0 {
		t.Fatalf("third step: remaining %d, want done", b.Remaining())
	}
}

// T6b: the marker lives in ledger.db, not with the watcher's cursors — remove
// the ledger and the next start refills it from the transcripts on disk.
func TestBackfillRefillsADeletedLedger(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	roots := machine(t)
	list := func() []watch.Root { return roots }
	runToEnd(t, NewBackfill(ledger.New(), flatPrice, list))
	if err := os.Remove(filepath.Join(os.Getenv("KELD_HOME"), "state", "ledger.db")); err != nil {
		t.Fatal(err)
	}
	store := ledger.New()
	if store.RequestsBackfillDone() {
		t.Fatal("a fresh ledger inherited the marker")
	}
	runToEnd(t, NewBackfill(store, flatPrice, list))
	if got := len(allRows(t, store)); got != 10 {
		t.Fatalf("refilled %d, want 10", got)
	}
}

// A transcript that disappears between listing and reading is skipped, not an
// error that stops the backfill.
func TestBackfillSkipsAVanishedFile(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	roots := machine(t)
	b := NewBackfill(ledger.New(), flatPrice, func() []watch.Root { return roots })
	b.PerStep = 1
	b.Step() // lists, reads one
	if err := os.RemoveAll(roots[1].Dir); err != nil {
		t.Fatal(err)
	}
	runToEnd(t, b)
}

// A stopped backfill does not claim to be done: the marker stays unset, so the
// next run reads everything again.
func TestStoppedBackfillIsNotMarkedDone(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	store := ledger.New()
	roots := machine(t)
	b := NewBackfill(store, flatPrice, func() []watch.Root { return roots })
	b.stopped = func() bool { return true }
	b.Step()
	if store.RequestsBackfillDone() {
		t.Fatal("a stopped backfill set the marker")
	}
	if b.Remaining() != 3 {
		t.Fatalf("remaining %d, want all 3 files still to read", b.Remaining())
	}
}
