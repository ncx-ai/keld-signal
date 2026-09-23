package integrations

import (
	"testing"
	"time"
)

// ⚠️ A LANE THE PERSON SWITCHED OFF IS NOT A LANE OF THIS MACHINE. The `otel`
// surface was published on every tool whatever the switch said, so the shipped
// default — switch off — put an `otel` row on every row of the pane, forever,
// for something deliberately not in use. Reported as "why would I care about
// otel if I know otel is turned off".
func TestTheOTelLaneIsNotPublishedWhileTheSwitchIsOff(t *testing.T) {
	f := configured()
	f.Wiring.ConfigMatchesAdapter = true
	f.Wiring.ConfiguredAt = now.Add(-2 * time.Hour)
	f.Wiring.HookTrusted, f.Wiring.HookTrustKnown = true, true

	rows := Compute(now, []Entry{entry(t, "claude_code")},
		map[string]Facts{"claude_code": f}, Options{Window: 24 * time.Hour, ToolOTLP: false})
	for _, s := range rows[0].Surfaces {
		if s.Kind == SurfaceOTel {
			t.Fatal("the otel lane is published while the switch is off; the pane then renders a row for a lane nobody asked for")
		}
	}

	// And it comes back the moment the switch does — the Developer box is how a
	// person turns the lane on, so the pane has to show it again.
	on := Compute(now, []Entry{entry(t, "claude_code")},
		map[string]Facts{"claude_code": f}, Options{Window: 24 * time.Hour, ToolOTLP: true})
	var found bool
	for _, s := range on[0].Surfaces {
		if s.Kind == SurfaceOTel {
			found = true
		}
	}
	if !found {
		t.Error("the otel lane is missing with the switch ON, so turning it on shows nothing")
	}
}

// ⚠️ NARROWER THAN "HIDE WHAT IS NOT EXPECTED", and the difference is who
// decided. A lane that cannot feed for STRUCTURAL reasons stays visible, so
// that a lane with no traffic and a lane that could never have any do not look
// alike. Cowork rides Claude Desktop and never exports OTLP itself; that is a
// fact about the tool, not a switch position, and hiding it would lose it.
func TestAStructurallyImpossibleLaneIsStillPublished(t *testing.T) {
	e := entry(t, "cowork")
	rows := Compute(now, []Entry{e}, map[string]Facts{"cowork": configured()},
		Options{Window: 24 * time.Hour, ToolOTLP: false})

	var kinds int
	for _, spec := range e.Surfaces {
		for _, s := range rows[0].Surfaces {
			if s.Kind == spec.Kind {
				kinds++
			}
		}
	}
	if kinds != len(e.Surfaces) {
		t.Errorf("cowork published %d of its %d catalogue surfaces; a lane that could never feed must still be shown",
			kinds, len(e.Surfaces))
	}
}
