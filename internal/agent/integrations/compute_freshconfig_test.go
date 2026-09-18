package integrations

import (
	"testing"
	"time"
)

// ⚠️ ACTIVITY FROM BEFORE THE CONFIG WAS WRITTEN IS NOT EVIDENCE ABOUT THAT
// CONFIG, and counting it reports a tool broken seconds after Signal set it up.
//
// Reproduced by the conformance chain on 2026-09-15, for both Codex and Claude
// Code: the tool runs, Signal is installed, the detector configures it, and the
// very next poll reads `broken` — because telemetry the tool sent BEFORE it was
// configured supplied the "one expected lane saw it" half, while the hook,
// wired one second ago, had not yet had a chance to fire.
//
// AC-4's rule is that idle is never broken. A lane that has existed for one
// second has not been silent; it has not been asked. So the look-back is
// clamped: `cut = max(now-window, configMtime)`.
//
// This does NOT slow a real detection down. A tool configured an hour ago whose
// telemetry flows and whose hook never fires still reads broken within the hour,
// because that telemetry lands after the config.
func TestActivityBeforeTheConfigIsNotEvidenceAboutIt(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }

	f := configured()
	f.Wiring.ConfigMtime = now.Add(-2 * time.Second) // the detector just wrote it
	f.Wiring.NewestSessionStart = now.Add(-1 * time.Second)
	f.Wiring.HookTrusted, f.Wiring.HookTrustKnown = true, true
	// Telemetry from BEFORE setup. The hook has never fired, having existed for
	// two seconds.
	f.Lanes = LaneFacts{LastTelemetryForward: at(-1 * time.Hour)}

	got := one(t, entry(t, "claude_code"), f)
	if got.State == Broken {
		t.Fatalf("state = %q on a tool configured 2s ago; broken_lane=%q. "+
			"Pre-config telemetry supplied the active half while a two-second-old hook supplied the silent one.",
			got.State, got.BrokenLane)
	}
	if got.State != Idle {
		t.Errorf("state = %q, want %q — nothing has been seen since the config was written", got.State, Idle)
	}
}

// The other side, so the clamp cannot be mistaken for a blanket grace period:
// once activity lands AFTER the config, a silent expected lane still breaks the
// tool, and it does so inside the window rather than a day later.
func TestActivityAfterTheConfigStillBreaksASilentLane(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }

	f := configured()
	f.Wiring.ConfigMtime = now.Add(-2 * time.Hour)
	f.Wiring.NewestSessionStart = now.Add(-90 * time.Minute)
	f.Wiring.HookTrusted, f.Wiring.HookTrustKnown = true, true
	f.Lanes = LaneFacts{LastTelemetryForward: at(-30 * time.Minute)} // after the config

	got := one(t, entry(t, "claude_code"), f)
	if got.State != Broken {
		t.Fatalf("state = %q, want %q — telemetry has flowed since the config and the hook never fired", got.State, Broken)
	}
	if got.BrokenLane != SurfaceHook {
		t.Errorf("broken_lane = %q, want %q", got.BrokenLane, SurfaceHook)
	}
}

// A machine whose config mtime cannot be read (zero) falls back to the plain
// window. Unknown must not become a grace period that silences broken forever.
func TestAnUnknownConfigMtimeDoesNotSuppressBroken(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *time.Time { u := now.Add(d); return &u }

	f := configured()
	f.Wiring.ConfigMtime = time.Time{} // unreadable
	f.Wiring.NewestSessionStart = now.Add(-90 * time.Minute)
	f.Wiring.HookTrusted, f.Wiring.HookTrustKnown = true, true
	f.Lanes = LaneFacts{LastTelemetryForward: at(-30 * time.Minute)}

	if got := one(t, entry(t, "claude_code"), f); got.State != Broken {
		t.Fatalf("state = %q, want %q — an unreadable config mtime must not suppress a real break", got.State, Broken)
	}
}
