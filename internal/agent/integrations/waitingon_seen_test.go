package integrations

import (
	"testing"
	"time"
)

// ⚠️ A LANE THAT HAS JUST DELIVERED IS NOT WAITING FOR A RESTART. The hint was
// decided from the TOOL'S state alone, so every expected lane read "not
// restarted since" whenever the tool read `restart_required`.
//
// Seen on a real machine 2026-09-21: the hook lane labelled "not restarted
// since" while its last pointer was 31 SECONDS old, against a config written 11
// minutes earlier. The tool row was right — a session did predate the config —
// but the lane label is a statement about that lane, and it was false.
func TestALaneSeenSinceTheConfigIsNotWaitingForARestart(t *testing.T) {
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }

	f := configured()
	f.Wiring.ConfigMatchesAdapter = true
	f.Wiring.ConfiguredAt = now.Add(-11 * time.Minute)
	f.Wiring.NewestSessionStart = now.Add(-2 * time.Hour) // predates the config: the tool must restart
	f.Wiring.HookTrusted, f.Wiring.HookTrustKnown = true, true
	f.Lanes = LaneFacts{LastHookPointer: at(-31 * time.Second)} // delivered moments ago

	got := one(t, entry(t, "claude_code"), f)
	if got.State != RestartRequired {
		t.Fatalf("state = %q, want %q — the fixture is meant to reproduce the real row", got.State, RestartRequired)
	}
	for _, s := range got.Surfaces {
		if s.Kind == SurfaceHook && s.WaitingOn == WaitingOnRestart {
			t.Fatalf("the hook lane says it is waiting for a restart, 31s after it last delivered "+
				"(config written 11m ago); the restart belongs to the TOOL, not to this lane (waiting_on=%q)", s.WaitingOn)
		}
	}
}

// The other side: a lane that has NOT been seen since the config is exactly what
// the hint is for, and it must survive.
func TestALaneSilentSinceTheConfigStillAsksForARestart(t *testing.T) {
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }

	f := configured()
	f.Wiring.ConfigMatchesAdapter = true
	f.Wiring.ConfiguredAt = now.Add(-11 * time.Minute)
	f.Wiring.NewestSessionStart = now.Add(-2 * time.Hour)
	f.Wiring.HookTrusted, f.Wiring.HookTrustKnown = true, true
	f.Lanes = LaneFacts{LastHookPointer: at(-3 * time.Hour)} // last seen well before the config

	var found bool
	for _, s := range one(t, entry(t, "claude_code"), f).Surfaces {
		if s.Kind == SurfaceHook && s.WaitingOn == WaitingOnRestart {
			found = true
		}
	}
	if !found {
		t.Error("the hook lane has not been seen since the config was written and no longer says so")
	}
}
