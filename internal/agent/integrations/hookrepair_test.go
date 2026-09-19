package integrations

import (
	"testing"
	"time"
)

// TestABrokenHookCommandReadsAsNotConfigured — the repair path for a machine an
// OLDER keld configured. An upgrade preserves tool configs by design, so this
// is the only way the quoting fix reaches one.
func TestABrokenHookCommandReadsAsNotConfigured(t *testing.T) {
	e := Entry{ID: "claude_code", AdapterName: "claude_code", Supported: true}
	f := Facts{
		Configured: true, // the manifest says configured, and it IS — badly
		Wiring:     WiringFacts{ConfigPresent: true, HookCommandBroken: true},
	}
	if got, _ := decide(time.Now(), e, f, map[SurfaceKind]bool{}, map[SurfaceKind]laneState{}); got != NotConfigured {
		t.Errorf("a broken hook command must read as %s, got %s", NotConfigured, got)
	}
}

// TestAHealthyHookCommandIsLeftAlone — the same row with nothing wrong must not
// be dragged back to not_configured, or the detector rewrites every config on
// every poll forever.
func TestAHealthyHookCommandIsLeftAlone(t *testing.T) {
	e := Entry{ID: "claude_code", AdapterName: "claude_code", Supported: true}
	f := Facts{
		Configured: true,
		Wiring:     WiringFacts{ConfigPresent: true, HookCommandBroken: false},
	}
	if got, _ := decide(time.Now(), e, f, map[SurfaceKind]bool{}, map[SurfaceKind]laneState{}); got == NotConfigured {
		t.Errorf("a healthy row must not read as %s", NotConfigured)
	}
}
