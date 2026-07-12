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
