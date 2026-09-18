package settings

import (
	"os"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// ⚠️ AN INSTALL MUST NOT MOVE THE TELEMETRY SECRET. `keld-agent install` writes
// agent-config.json before anything else and a re-install is an ordinary event —
// every upgrade is one. A secret that changed there would 401 every AI tool
// configured by the previous install until a human re-ran setup AND restarted
// each tool, which is the 2026-09-18 outage (see paths.TelemetrySecretPath).
//
// Byte-identical, not merely equal: `previous`/`rotated_at` appearing would mean
// something took the rotation path, and a tool reconfigured after the grace
// window would then be locked out.
func TestInstallConfigWriteLeavesTheTelemetrySecretByteIdentical(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if _, err := agentcfg.EnsureTelemetrySecret(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(paths.TelemetrySecretPath())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if err := WriteInstallDefaults("deterministic", true); err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
	}
	after, err := os.ReadFile(paths.TelemetrySecretPath())
	if err != nil {
		t.Fatalf("ten installs removed the telemetry secret: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("telemetry secret changed across installs:\n before %s\n after  %s", before, after)
	}
}
