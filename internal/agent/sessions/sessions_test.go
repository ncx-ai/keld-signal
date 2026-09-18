package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, dir, name, body string, mod time.Time) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

func lead(n int) string {
	return strings.Repeat(`{"type":"custom-title","snapshot":{"timestamp":"1999-01-01T00:00:00Z"}}`+"\n", n)
}

func TestLiveExcludesSubagentTranscriptsAndClosedWindows(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	stamped := func(at time.Time) string {
		return lead(2) + `{"type":"user","timestamp":"` + at.UTC().Format(time.RFC3339Nano) + `"}` + "\n"
	}

	write(t, dir, "open.jsonl", stamped(now.Add(-time.Hour)), now.Add(-time.Minute))
	// ⚠️ The most recently written file here, and not a session: a subagent
	// transcript shares its parent's OTEL session id, so its name joins to
	// nothing and its first timestamp is when the SUBAGENT started.
	write(t, dir, "agent-sub.jsonl", stamped(now.Add(-time.Minute)), now)
	write(t, dir, "closed.jsonl", stamped(now.Add(-5*time.Hour)), now.Add(-2*time.Hour))
	write(t, dir, "notes.txt", "not a transcript", now)

	got := Live([]string{dir}, now)
	if len(got) != 1 || got[0].ID != "open" {
		ids := make([]string, len(got))
		for i, s := range got {
			ids[i] = s.ID
		}
		t.Fatalf("Live saw %v, want just [open]", ids)
	}
	if got[0].StartedAt.IsZero() {
		t.Error("StartedAt is zero for a transcript that carries a top-level timestamp")
	}
}

// ⚠️ A NESTED `timestamp` IS NOT THE RECORD'S OWN. `file-history-snapshot` and
// friends carry one and no top-level instant, so pattern-matching the first
// `"timestamp"` in the bytes returns a fabricated 1999 date here — the same trap
// `capture.scan` documents sidecar-side, where 1,135 of 73,449 real lines
// matched it.
func TestStartedAtDecodesRatherThanPatternMatches(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	real := now.Add(-3 * time.Hour).UTC().Truncate(time.Second)
	p := write(t, dir, "s.jsonl",
		lead(3)+`{"type":"user","timestamp":"`+real.Format(time.RFC3339Nano)+`"}`+"\n", now)

	if got := StartedAt(p); !got.Equal(real) {
		t.Fatalf("StartedAt = %v, want %v — a nested timestamp was read as the session's own", got, real)
	}
}

// The head bound is the larger of the two the callers used, because too tight
// silently starts answering UNKNOWN the day Claude Code adds another
// untimestamped bookkeeping record at the top of a transcript.
func TestStartedAtLooksPastTheOldFortyRecordBound(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	real := now.Add(-time.Hour).UTC().Truncate(time.Second)
	p := write(t, dir, "s.jsonl",
		lead(60)+`{"type":"user","timestamp":"`+real.Format(time.RFC3339Nano)+`"}`+"\n", now)

	if got := StartedAt(p); !got.Equal(real) {
		t.Fatalf("StartedAt = %v after 60 bookkeeping records, want %v", got, real)
	}
}

// Zero is a first-class answer: the caller draws no conclusion rather than
// treating an unreadable head as "started long ago".
func TestAnUndatedTranscriptStartsAtZero(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	p := write(t, dir, "s.jsonl", lead(5), now)
	if got := StartedAt(p); !got.IsZero() {
		t.Fatalf("StartedAt = %v for a transcript with no top-level timestamp; zero means UNKNOWN", got)
	}
}

// Two roots that overlap must not produce the same session twice — the pane
// asks "which windows are open", and a duplicate would read as two.
func TestOverlappingRootsYieldOneSightingPerFile(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	sub := filepath.Join(root, "projects")
	write(t, sub, "one.jsonl",
		lead(1)+`{"type":"user","timestamp":"`+now.Add(-time.Hour).UTC().Format(time.RFC3339Nano)+`"}`+"\n",
		now.Add(-time.Minute))

	if got := Live([]string{root, sub}, now); len(got) != 1 {
		t.Fatalf("Live returned %d sightings for one file under two overlapping roots", len(got))
	}
}
