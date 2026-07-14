package cli

import (
	"bytes"
	"testing"
)

func TestSignalMetricsCmdErrorsWhenDaemonDown(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir()) // no agent.json → daemon down
	cmd := newSignalMetricsCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.RunE(cmd, nil); err == nil {
		t.Fatal("want error when keld-agent is not running")
	}
}

// TestSignalEnrichCmdDeterministicErrors pins the no-fallback contract on the
// CLI diagnostic path: the deterministic backend has been removed, so
// --deterministic can no longer silently produce lower-fidelity output — it
// must error instead. (Removing the flag itself, so this stops being a valid
// invocation at all, is tracked separately.)
func TestSignalEnrichCmdDeterministicErrors(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	cmd := newSignalEnrichCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.Flags().Set("deterministic", "true")
	if err := cmd.RunE(cmd, []string{"refactor the auth module"}); err == nil {
		t.Fatal("want error: --deterministic is no longer supported (no deterministic backend)")
	}
}
