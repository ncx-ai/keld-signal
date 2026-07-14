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

// TestSignalEnrichCmdErrorsWhenSidecarDown pins the no-fallback contract on
// the CLI diagnostic path: ML is mandatory, so with no keld-agent/sidecar
// running the command must error instead of silently degrading to a
// lower-fidelity backend.
func TestSignalEnrichCmdErrorsWhenSidecarDown(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	cmd := newSignalEnrichCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.RunE(cmd, []string{"refactor the auth module"}); err == nil {
		t.Fatal("want error when the sidecar is not running (no fallback backend)")
	}
}
