package integrations

import (
	"testing"
	"time"
)

// ⚠️ A LANE THAT HAS NOT HAD TIME TO REPORT IS NOT A SILENT LANE. `broken`
// needs one expected lane active and another silent, and it was deciding that
// the instant the first one fired — so seconds after a prompt the hook lane had
// reported while the reader lane was still being ingested by the sidecar, and a
// perfectly healthy machine read `broken · reader`.
//
// Observed by the day-three conformance chain on 2026-09-19, which reported it
// rather than failing on it: "reads broken seconds after a live prompt". It is
// AC-4's rule one step along — a quiet user is not a bug, and neither is a busy
// one whose second lane is still in flight.
func TestALaneStillInFlightDoesNotBreakTheTool(t *testing.T) {
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }
	rows := false

	f := configured()
	f.Wiring.ConfigMatchesAdapter = true
	f.Wiring.ConfiguredAt = now.Add(-2 * time.Hour)
	f.Wiring.NewestSessionStart = now.Add(-time.Hour)
	f.Wiring.HookTrusted, f.Wiring.HookTrustKnown = true, true
	// The prompt landed ten seconds ago: the hook has posted its pointer, and
	// the reader lane has not been asked yet.
	f.Lanes = LaneFacts{
		LastHookPointer:       at(-10 * time.Second),
		LastWatcherPointer:    at(-10 * time.Second),
		LastTelemetryForward:  at(-10 * time.Second),
		RowsForRecentPointers: &rows,
	}

	if got := one(t, entry(t, "claude_code"), f); got.State == Broken {
		t.Fatalf("state = %q (%s) ten seconds after a live prompt; nothing has had time to go silent",
			got.State, got.BrokenLane)
	}
}

// The other side, so the settling window cannot be mistaken for an amnesty: once
// the activity is old enough that the silent lane would have reported, silence
// is evidence again and the tool is broken.
func TestOnceTheDustSettlesASilentLaneStillBreaksTheTool(t *testing.T) {
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }
	rows := false

	f := configured()
	f.Wiring.ConfigMatchesAdapter = true
	f.Wiring.ConfiguredAt = now.Add(-4 * time.Hour)
	f.Wiring.NewestSessionStart = now.Add(-3 * time.Hour)
	f.Wiring.HookTrusted, f.Wiring.HookTrustKnown = true, true
	f.Lanes = LaneFacts{
		LastHookPointer:       at(-30 * time.Minute),
		LastWatcherPointer:    at(-30 * time.Minute),
		LastTelemetryForward:  at(-30 * time.Minute),
		RowsForRecentPointers: &rows,
	}

	got := one(t, entry(t, "claude_code"), f)
	if got.State != Broken {
		t.Fatalf("state = %q half an hour after the last activity, want %q — the reader lane has had every chance to report",
			got.State, Broken)
	}
}
