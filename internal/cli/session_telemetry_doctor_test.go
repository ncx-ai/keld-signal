package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/sessions"
	"github.com/ncx-ai/keld-signal/internal/localagent"
)

// writeTranscript lays down one Claude Code transcript: `lead` untimestamped
// bookkeeping records (Claude Code opens a file with `custom-title`, `mode`,
// `aiTitle` and `file-history-snapshot`, and invents more over time), then the
// first record that carries a top-level timestamp.
func writeTranscript(t *testing.T, dir, id string, lead int, start, mod time.Time) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for i := 0; i < lead; i++ {
		b.WriteString(`{"type":"custom-title","snapshot":{"timestamp":"1999-01-01T00:00:00Z"}}` + "\n")
	}
	b.WriteString(`{"type":"user","timestamp":"` + start.UTC().Format(time.RFC3339Nano) + `"}` + "\n")
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatal(err)
	}
}

// ⚠️ **THE PANE AND DOCTOR BOTH NAME A STALE SESSION, SO THEY MUST AGREE ON
// WHICH SESSIONS EXIST.** They used to walk transcripts separately — the
// integrations reader looking 40 records into a file for its start instant,
// doctor 200 — which is a disagreement waiting for Claude Code to add one more
// untimestamped record at the top of a transcript: the pane would then read the
// session as UNKNOWN and refuse to call it stale, while doctor read it fine and
// reported it. One machine, two answers, neither of them wrong on its own terms.
//
// Both now resolve it through `agent/sessions`, and this asserts the result is
// the same set rather than merely that the code looks shared.
func TestThePaneAndDoctorAgreeOnWhichSessionsAreLive(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()

	// A deep head — past the 40 records the pane's own reader used to stop at.
	writeTranscript(t, dir, "deep-head-window", 60, now.Add(-2*time.Hour), now.Add(-2*time.Minute))
	writeTranscript(t, dir, "shallow-window", 2, now.Add(-40*time.Minute), now.Add(-time.Second))
	// A subagent transcript: the most recently written file here, and not a
	// session at all. Neither surface may see it.
	writeTranscript(t, dir, "agent-subtask", 2, now.Add(-5*time.Minute), now)
	// Closed hours ago: inside doctor's scan window, outside the live rule.
	writeTranscript(t, dir, "closed-window", 2, now.Add(-5*time.Hour), now.Add(-90*time.Minute))

	shared := sessions.Live([]string{dir}, now)
	sharedIDs := map[string]time.Time{}
	for _, s := range shared {
		sharedIDs[s.ID] = s.StartedAt
	}
	if len(sharedIDs) != 2 || sharedIDs["deep-head-window"].IsZero() || sharedIDs["shallow-window"].IsZero() {
		t.Fatalf("the shared reader saw %v; want both live sessions, with a known start instant each", sharedIDs)
	}

	// Doctor's side, through its own mapping of the same function.
	doctorLive := map[string]time.Time{}
	for _, s := range activeClaudeSessions([]string{dir}, now) {
		if now.Sub(s.LastSeen) > sessions.ActiveWindow {
			continue // doctor scans wider and applies the live rule itself
		}
		doctorLive[s.ID] = s.StartedAt
	}

	if len(doctorLive) != len(sharedIDs) {
		t.Fatalf("doctor sees %v, the shared reader sees %v", doctorLive, sharedIDs)
	}
	for id, start := range sharedIDs {
		got, ok := doctorLive[id]
		if !ok {
			t.Errorf("doctor does not see live session %q", id)
			continue
		}
		if !got.Equal(start) {
			t.Errorf("session %q starts at %v for doctor and %v for the pane; one machine, two answers", id, got, start)
		}
	}
}

// And the consequence that matters: the id doctor names is the id the pane's
// restart verdict would name, for the same machine and the same reason — a
// session still on the old config that has forwarded nothing.
func TestDoctorNamesTheSameStaleSessionThePaneWould(t *testing.T) {
	now := time.Now()
	dir := t.TempDir()
	writeTranscript(t, dir, "8f21c0de-stale", 60, now.Add(-2*time.Hour), now.Add(-2*time.Minute))
	writeTranscript(t, dir, "aa11bb22-fresh", 2, now.Add(-40*time.Minute), now.Add(-time.Second))

	state := localagent.SessionTelemetryState{
		Known:      true,
		Configured: true,
		Active:     activeClaudeSessions([]string{dir}, now),
		Forwarded:  map[string]time.Time{"aa11bb22-fresh": now.Add(-time.Minute)},
		Now:        now,
	}
	line := state.ProblemLine()
	if !strings.Contains(line, "8f21c0de") {
		t.Fatalf("doctor's line does not name the stale session: %q", line)
	}
	if strings.Contains(line, "aa11bb22") {
		t.Errorf("doctor named a session whose telemetry is arriving: %q", line)
	}
}
