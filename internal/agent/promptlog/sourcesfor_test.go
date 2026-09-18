package promptlog

import "testing"

// ⚠️ THE TWO PATHS ARE COMPLEMENTS, AND "NEITHER" IS THE FAILURE THIS PINS.
// A tool's usage reaches Atlas either because the tool posts its own OTLP (the
// `tool_otlp` switch wrote an OTEL block into its config) or because this
// mirror reads it out of the transcript. Exactly one, always one.
//
// Both halves shipped correct and the seam between them did not: the switch
// went off by default while this package still defaulted to {cowork}, so
// Claude Code and Codex emitted nothing at all — and the pane read `working`,
// because with the switch off the otel lane is not expected. The conformance
// chain caught it as "telemetry: 0 OTLP forwarded".
func TestTheMirrorIsTheComplementOfTheSwitch(t *testing.T) {
	for _, otlp := range []bool{false, true} {
		got := SourcesFor(otlp)
		for _, src := range []string{sourceClaudeCode, sourceCodex, sourceGemini} {
			if got[src] == otlp {
				state := "off"
				if otlp {
					state = "on"
				}
				if otlp {
					t.Errorf("tool_otlp %s and %s is ALSO mirrored: its usage would be counted twice", state, src)
				} else {
					t.Errorf("tool_otlp %s and %s is not mirrored either: that tool emits no usage at all", state, src)
				}
			}
		}
	}
}

// Cowork is outside the complement: its sandbox blocks its own egress, so the
// host-side mirror is the only path it has ever had, whatever the switch says.
func TestCoworkIsMirroredWhicheverWayTheSwitchIsSet(t *testing.T) {
	for _, otlp := range []bool{false, true} {
		if !SourcesFor(otlp)[sourceCowork] {
			t.Errorf("tool_otlp=%v dropped cowork, whose egress is blocked by design", otlp)
		}
	}
}

// An operator who named the set gets the set they named, in both positions —
// the env override is how a machine opts out of the rule entirely.
func TestTheEnvOverrideWinsOverTheComplement(t *testing.T) {
	t.Setenv("KELD_WATCH_TELEMETRY_SOURCES", "cowork")
	for _, otlp := range []bool{false, true} {
		got := SourcesFor(otlp)
		if got[sourceClaudeCode] || len(got) != 1 || !got[sourceCowork] {
			t.Errorf("tool_otlp=%v ignored the operator's explicit source list: %v", otlp, got)
		}
	}
	t.Setenv("KELD_WATCH_TELEMETRY", "off")
	if got := SourcesFor(false); len(got) != 0 {
		t.Errorf("KELD_WATCH_TELEMETRY=off still mirrored %v", got)
	}
}
