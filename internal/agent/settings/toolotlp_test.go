package settings

import "testing"

// The switch is OFF unless something on this machine says otherwise, and both
// directions of KELD_TOOL_OTLP win over the file — the shape every other local
// toggle in this package has (Features, Attribution, Blocks).
func TestToolOTLPDefaultsOffAndEnvWinsBothWays(t *testing.T) {
	if (Settings{}).ToolOTLPEnabled() {
		t.Error("a zero Settings reports the tool's OTLP lane ON; the default is off")
	}
	if !(Settings{ToolOTLP: true}).ToolOTLPEnabled() {
		t.Error("tool_otlp:true in the file did not turn the lane on")
	}

	t.Setenv(ToolOTLPEnv, "1")
	if !(Settings{}).ToolOTLPEnabled() {
		t.Error("KELD_TOOL_OTLP=1 did not turn the lane on over a file that is silent")
	}
	// The direction that matters more: a machine whose file says on must be
	// able to turn it off without editing the file, because this is the switch
	// someone flips while deciding whether the lane can be removed at all.
	t.Setenv(ToolOTLPEnv, "0")
	if (Settings{ToolOTLP: true}).ToolOTLPEnabled() {
		t.Error("KELD_TOOL_OTLP=0 did not beat tool_otlp:true in the file")
	}
	t.Setenv(ToolOTLPEnv, "")
	if !(Settings{ToolOTLP: true}).ToolOTLPEnabled() {
		t.Error("an empty KELD_TOOL_OTLP is 'unset', not 'off'")
	}
}
