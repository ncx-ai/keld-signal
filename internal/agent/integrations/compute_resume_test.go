package integrations

import (
	"testing"
	"time"
)

// ⚠️ A RESUMED SESSION KEEPS ITS TRANSCRIPT, SO ITS START INSTANT NEVER MOVES —
// and `restart_required` read off that instant can therefore never clear, on a
// tool the person has genuinely restarted. The instruction beside the row asks
// for something that does not work, which is worse than no instruction.
//
// Measured on a real machine: config written 15:06:55Z, the resumed session's
// first line at 14:46Z, and that session forwarding telemetry at 15:13:01Z —
// through the loopback proxy the new config points at, which the process could
// only reach by having read it. The row said `restart_required` across two
// genuine restarts.
func TestARestartedSessionClearsRestartRequired(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }

	f := configured()
	f.Wiring.ConfiguredAt = now.Add(-30 * time.Minute)
	f.Wiring.NewestSessionStart = now.Add(-2 * time.Hour) // the resumed transcript's first line
	f.Wiring.NewestSessionAdopted = true                  // it has forwarded since the config
	f.Wiring.HookTrusted, f.Wiring.HookTrustKnown = true, true
	f.Lanes = LaneFacts{
		LastTelemetryForward: at(-5 * time.Minute),
		LastHookPointer:      at(-5 * time.Minute),
		LastWatcherPointer:   at(-5 * time.Minute),
	}

	if got := one(t, entry(t, "claude_code"), f); got.State == RestartRequired {
		t.Fatalf("state = %q on a session that has forwarded telemetry since the config was written; "+
			"restarting the tool cannot clear this, because a resume keeps the transcript the start instant is read from", got.State)
	}
}

// The other side: an unadopted stale session still says restart. The exemption
// is evidence of a restart, not a blanket amnesty for an old start instant.
func TestAStaleSessionThatHasSentNothingStillSaysRestart(t *testing.T) {
	now := time.Now()

	f := configured()
	f.Wiring.ConfiguredAt = now.Add(-30 * time.Minute)
	f.Wiring.NewestSessionStart = now.Add(-2 * time.Hour)
	f.Wiring.NewestSessionAdopted = false
	f.Wiring.HookTrusted, f.Wiring.HookTrustKnown = true, true

	if got := one(t, entry(t, "claude_code"), f); got.State != RestartRequired {
		t.Errorf("state = %q, want %q — the session predates the config and has produced no evidence of a restart", got.State, RestartRequired)
	}
}
