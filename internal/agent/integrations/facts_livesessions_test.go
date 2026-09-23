package integrations

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSession lays down one transcript with a first-line timestamp and a
// chosen mtime, the way a tool leaves one on disk.
func writeSession(t *testing.T, dir, id string, start, mod time.Time) string {
	t.Helper()
	path := filepath.Join(dir, id+".jsonl")
	body := `{"type":"custom-title"}` + "\n" +
		`{"type":"user","timestamp":"` + start.UTC().Format(time.RFC3339Nano) + `"}` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
	return path
}

// ⚠️ THE ROW MUST NOT FLICKER BETWEEN TWO LIVE SESSIONS. Picking the newest
// TRANSCRIPT means a machine with one restarted window and one carried over
// from yesterday reports whichever wrote last — so the state alternates every
// few seconds with nothing about the machine having changed, and the person is
// told to restart a tool they just restarted. Observed here on 2026-09-18,
// between a session started 15 minutes earlier and one started the previous
// day, both live.
//
// A live session still on the old config wins the report whichever file is
// newest, which is both the actionable answer and a stable one.
func TestAStaleLiveSessionWinsOverANewerAdoptedOne(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	configMtime := now.Add(-time.Hour)

	writeSession(t, dir, "stale", now.Add(-24*time.Hour), now.Add(-2*time.Minute))
	writeSession(t, dir, "fresh", now.Add(-30*time.Minute), now.Add(-1*time.Second)) // newest file

	d := Deps{
		Now:            func() time.Time { return now },
		TranscriptDirs: func(Entry) []string { return []string{dir} },
		// Only the fresh session has forwarded since the config.
		SessionForward: func(id string) *time.Time {
			if id == "fresh" {
				u := now.Add(-30 * time.Second)
				return &u
			}
			return nil
		},
	}.withDefaults()

	start, adopted, _ := restartFacts(d, Entry{ID: "claude_code"}, configMtime)
	if adopted {
		t.Fatal("reported adopted while a live session is still on the old config")
	}
	if !start.Before(configMtime) {
		t.Errorf("start %v is not the stale session's; the newest file won the report and the row will flicker", start)
	}
}

// The other side: with every live session restarted, the row is not held open
// by a transcript that has gone quiet. A session nobody is writing to any more
// cannot be restarted, and a finding that can never clear is one people learn
// to ignore.
func TestAQuietStaleSessionDoesNotHoldTheRowOpen(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	configMtime := now.Add(-time.Hour)

	writeSession(t, dir, "yesterday", now.Add(-24*time.Hour), now.Add(-3*time.Hour)) // long quiet
	writeSession(t, dir, "fresh", now.Add(-30*time.Minute), now.Add(-1*time.Second))

	d := Deps{
		Now:            func() time.Time { return now },
		TranscriptDirs: func(Entry) []string { return []string{dir} },
		SessionForward: func(id string) *time.Time {
			if id == "fresh" {
				u := now.Add(-30 * time.Second)
				return &u
			}
			return nil
		},
	}.withDefaults()

	if _, adopted, _ := restartFacts(d, Entry{ID: "claude_code"}, configMtime); !adopted {
		t.Error("a transcript quiet for three hours held the restart notice open")
	}
}
