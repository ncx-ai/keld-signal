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

func TestSignalEnrichCmdDeterministicPrintsJSON(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	cmd := newSignalEnrichCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.Flags().Set("deterministic", "true") // no sidecar needed
	if err := cmd.RunE(cmd, []string{"refactor the auth module"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	if !bytes.Contains(out.Bytes(), []byte(`"task_type"`)) {
		t.Fatalf("expected a profile JSON, got: %s", out.String())
	}
}
