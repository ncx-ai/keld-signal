package cli

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/config"
)

// ⚠️ **OFF MEANS NOTHING IS BLAMED, AND DOCTOR IS WHERE THAT WOULD BREAK FIRST.**
// Both telemetry findings turn on `Configured`, which their own doc defines as
// "whether tools are set up to send telemetry at all". With `tool_otlp` off keld
// writes no OTEL block, so they are not — and a machine that HAD the lane on
// keeps a non-empty forward record, which is exactly the state in which both
// checks would otherwise fire: the machine-wide one because nothing has arrived
// since the credential was written, the per-session one because no running
// session appears in a record that is no longer being added to. Both would tell
// a person to restart their editor to fix a lane that was switched off on
// purpose, which is advice that cannot work.
func TestTelemetryFindingsAreSilentWhileTheOTLPLaneIsOff(t *testing.T) {
	m := &config.Manifest{Tools: map[string]config.ToolManifest{
		"claude_code": {Name: "claude_code"},
	}}

	t.Setenv(settings.ToolOTLPEnv, "1")
	if !telemetryState(m).Configured {
		t.Error("with the lane on, the machine-wide check does not consider the tools configured for telemetry")
	}
	if !sessionTelemetryState(m).Configured {
		t.Error("with the lane on, the per-session check does not consider the tools configured for telemetry")
	}

	t.Setenv(settings.ToolOTLPEnv, "0")
	if telemetryState(m).Configured {
		t.Error("the machine-wide telemetry check still judges a machine that writes no OTEL block")
	}
	if sessionTelemetryState(m).Configured {
		t.Error("the per-session telemetry check still judges a machine that writes no OTEL block")
	}
	// And with Configured false neither can produce a line, whatever else is on
	// disk — the guard both ProblemLine implementations already have.
	if p := telemetryState(m).ProblemLine(); p != "" {
		t.Errorf("machine-wide finding with the lane off: %q", p)
	}
	if p := sessionTelemetryState(m).ProblemLine(); p != "" {
		t.Errorf("per-session finding with the lane off: %q", p)
	}
}
