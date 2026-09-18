package integrations

import (
	"testing"
	"time"
)

// ⚠️ "RESTART THIS TOOL" IS NOT AN INSTRUCTION WHEN TWO WINDOWS ARE OPEN, and
// on 2026-09-18 two open Claude Code windows on the maintainer's machine were
// exactly what made the row unreadable — it alternated between them every few
// seconds until the rule was widened to ask about every live session. Widening
// it made the row stable; it did not make the row SAY which window.
//
// The rule already knows: `restartFacts` walks the live sessions and returns on
// the one that is stale. This publishes that id, and only under the verdict it
// caused.
func TestStaleSessionIDIsPublishedOnlyUnderRestartRequired(t *testing.T) {
	stale := func() Facts {
		f := configured()
		f.Wiring.ConfiguredAt = now.Add(-30 * time.Minute)
		f.Wiring.NewestSessionStart = now.Add(-2 * time.Hour)
		f.Wiring.StaleSessionID = "8f21c0de-1111-2222-3333-444455556666"
		f.Wiring.HookTrusted, f.Wiring.HookTrustKnown = true, true
		return f
	}

	got := one(t, entry(t, "claude_code"), stale())
	if got.State != RestartRequired {
		t.Fatalf("state = %q, want %q — this fixture is the stale-session machine", got.State, RestartRequired)
	}
	if got.StaleSessionID != "8f21c0de-1111-2222-3333-444455556666" {
		t.Errorf("stale_session_id = %q, want the session the verdict was decided on; "+
			"without it the row asks a person with two windows open to guess which one", got.StaleSessionID)
	}

	// The same machine one restart later. The verdict is gone, so the evidence
	// for it must go too — a `working` row carrying a stale session id would be
	// stating a problem it has just decided there isn't.
	f := stale()
	f.Wiring.NewestSessionAdopted = true
	f.Lanes = LaneFacts{
		LastHookPointer:       ago(5 * time.Minute),
		LastWatcherPointer:    ago(5 * time.Minute),
		LastTelemetryForward:  ago(5 * time.Minute),
		RowsForRecentPointers: yes(),
	}
	got = one(t, entry(t, "claude_code"), f)
	if got.State == RestartRequired {
		t.Fatalf("state = %q — this fixture has adopted the config", got.State)
	}
	if got.StaleSessionID != "" {
		t.Errorf("stale_session_id = %q on a %q row; the id may only accompany the verdict it caused",
			got.StaleSessionID, got.State)
	}
}

// And the reader end: with two live sessions and only one of them stale, the id
// on the row is the STALE one — not the newest file, which is what the row used
// to be decided by.
func TestTheNamedSessionIsTheStaleOneNotTheNewestFile(t *testing.T) {
	dir := t.TempDir()
	configuredAt := now.Add(-time.Hour)

	writeSession(t, dir, "stale-window", now.Add(-24*time.Hour), now.Add(-2*time.Minute))
	writeSession(t, dir, "fresh-window", now.Add(-30*time.Minute), now.Add(-time.Second)) // newest file

	d := Deps{
		Now:            func() time.Time { return now },
		TranscriptDirs: func(Entry) []string { return []string{dir} },
		SessionForward: func(id string) *time.Time {
			if id == "fresh-window" {
				u := now.Add(-30 * time.Second)
				return &u
			}
			return nil
		},
	}.withDefaults()

	_, _, staleID := restartFacts(d, Entry{ID: "claude_code"}, configuredAt)
	if staleID != "stale-window" {
		t.Errorf("stale session = %q, want %q — the id must name the window still on the old config, "+
			"not whichever transcript was written most recently", staleID, "stale-window")
	}
}
