package integrations

import (
	"testing"
	"time"
)

// ⚠️ WITH THE OTLP LANE OFF, A SESSION THAT PREDATES THE CONFIG HAS NOTHING
// STALE TO FIX. A tool reads two things from its config at startup: the hook
// command and the OTEL block. With `tool_otlp` off no OTEL block is written at
// all, the hook command is unchanged, and the daemon reads the transcript
// regardless — so restarting changes nothing a person would notice.
//
// The row said otherwise on a real machine 2026-09-21: hook ✓ watcher ✓ reader
// ✓, state `restart_required`, and — after the lane-hint fix — no sentence under
// it saying why. Reported as "why is the restart required if everything is
// healthy now". WS6's adoption escape could not clear it either: it accepts only
// TELEMETRY from that session as proof, and a machine with the switch off asks
// the tool for none.
func TestNoRestartIsAskedForWhenTheSwitchIsOffAndTheHookIsLive(t *testing.T) {
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }

	f := configured()
	f.Wiring.ConfigMatchesAdapter = true
	f.Wiring.ConfiguredAt = now.Add(-30 * time.Minute)
	f.Wiring.NewestSessionStart = now.Add(-3 * time.Hour) // predates the config
	f.Wiring.HookTrusted, f.Wiring.HookTrustKnown = true, true
	rows := true
	f.Lanes = LaneFacts{
		LastHookPointer:       at(-time.Minute), // delivering since the config
		LastWatcherPointer:    at(-time.Minute),
		RowsForRecentPointers: &rows,
	}

	// Computed with the switch OFF — the shipped default, and the whole point.
	// `one()` computes with ToolOTLP true, where the notice deliberately still
	// wins (see configReadLanesStale on vouching).
	rowsOut := Compute(now, []Entry{entry(t, "claude_code")},
		map[string]Facts{"claude_code": f}, Options{Window: 24 * time.Hour, ToolOTLP: false})
	got := rowsOut[0]
	if got.State == RestartRequired {
		t.Fatalf("state = %q with every expected lane live and the OTLP lane off; "+
			"a restart fixes nothing a person would notice", got.State)
	}
}

// The case the notice was built for still fires: a session started before setup
// that is posting nowhere. The hook has been silent since the config, so there
// IS something a restart fixes.
func TestARestartIsStillAskedForWhenTheHookHasGoneSilent(t *testing.T) {
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }

	f := configured()
	f.Wiring.ConfigMatchesAdapter = true
	f.Wiring.ConfiguredAt = now.Add(-30 * time.Minute)
	f.Wiring.NewestSessionStart = now.Add(-3 * time.Hour)
	f.Wiring.HookTrusted, f.Wiring.HookTrustKnown = true, true
	f.Lanes = LaneFacts{LastHookPointer: at(-3 * time.Hour)} // nothing since the config

	out := Compute(now, []Entry{entry(t, "claude_code")},
		map[string]Facts{"claude_code": f}, Options{Window: 24 * time.Hour, ToolOTLP: false})
	if got := out[0]; got.State != RestartRequired {
		t.Errorf("state = %q, want %q — the hook has not been seen since the config was written", got.State, RestartRequired)
	}
}
