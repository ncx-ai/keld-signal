package integrations

import (
	"testing"
	"time"
)

// ⚠️ **WITH THE SWITCH OFF THE otel LANE IS NOT EXPECTED, AND THAT IS THE WHOLE
// MECHANISM.** `broken` needs one expected lane active and another expected lane
// silent, so a lane missing from ExpectedLanes can contribute NEITHER half — the
// rule `Entry.ExpectedLanes` already carries. Nothing new decides anything; the
// switch only moves a lane in and out of that one list.
func TestOTelIsNotExpectedWhileTheSwitchIsOff(t *testing.T) {
	for _, id := range []string{"claude_code", "codex", "gemini_cli"} {
		e, ok := Get(id)
		if !ok {
			t.Fatalf("catalogue has no entry %q", id)
		}
		off := SupportLevel{Supported: true, ReaderAvailable: e.ReaderAvailable}
		for _, k := range e.ExpectedLanes(off) {
			if k == SurfaceOTel {
				t.Errorf("%s expects the otel lane with tool_otlp off", id)
			}
		}
		on := off
		on.ToolOTLP = true
		var found bool
		for _, k := range e.ExpectedLanes(on) {
			if k == SurfaceOTel {
				found = true
			}
		}
		if !found {
			t.Errorf("%s does not expect the otel lane with tool_otlp on", id)
		}
	}
	// Cowork's otel lane is `never` at any level: its egress to Atlas is blocked
	// by design, so the switch must not reach it in either position.
	cowork, _ := Get("cowork")
	for _, l := range []SupportLevel{
		{Supported: true, ReaderAvailable: true},
		{Supported: true, ReaderAvailable: true, ToolOTLP: true},
	} {
		for _, k := range cowork.ExpectedLanes(l) {
			if k == SurfaceOTel {
				t.Errorf("cowork expects otel at level %+v", l)
			}
		}
	}
}

// The state rule, end to end: the exact fact pattern that produces `broken · otel`
// with the lane on must produce something else with it off — and it must not be
// producible by any silence at all.
func TestNoFactsProduceBrokenOTelWhileTheSwitchIsOff(t *testing.T) {
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Minute)
	e, _ := Get("claude_code")

	// Hook, watcher and reader all seen; the otel lane silent. That is row 6 of
	// the decision table.
	rows := true
	f := Facts{
		Configured: true,
		Wiring: WiringFacts{
			ConfigPresent:        true,
			ConfigMatchesAdapter: true,
			ConfiguredAt:         now.Add(-2 * time.Hour),
			NewestSessionStart:   now.Add(-time.Hour),
		},
		Lanes: LaneFacts{
			LastHookPointer:       &recent,
			LastWatcherPointer:    &recent,
			RowsForRecentPointers: &rows,
		},
	}

	on := Compute(now, []Entry{e}, map[string]Facts{e.ID: f}, Options{ToolOTLP: true})[0]
	if on.State != Broken || on.BrokenLane != SurfaceOTel {
		t.Fatalf("with the lane on: state = %q · %q, want broken · otel — the fixture no longer reproduces row 6",
			on.State, on.BrokenLane)
	}

	off := Compute(now, []Entry{e}, map[string]Facts{e.ID: f}, Options{})[0]
	if off.State == Broken {
		t.Errorf("with the lane off: state = %q · %q; a lane keld does not write can never be the silent half",
			off.State, off.BrokenLane)
	}
	if off.State != Working {
		t.Errorf("with the lane off: state = %q, want working — every lane that IS expected was seen", off.State)
	}
	for _, s := range off.Surfaces {
		if s.Kind == SurfaceOTel && s.Expected {
			t.Error("the otel surface is still published as expected with the switch off")
		}
	}
}
