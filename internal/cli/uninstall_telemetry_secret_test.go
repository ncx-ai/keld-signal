package cli

import (
	"os"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// ⚠️ UNINSTALL REMOVES ~/.keld/state WHOLESALE, AND THE TELEMETRY SECRET MUST
// SURVIVE IT. `keld signal uninstall` with the last tool removed deletes
// hook.json and the whole state directory; a secret living under there would be
// regenerated on the next setup, and every tool NOT covered by that uninstall —
// on a machine where one tool was removed and others were left alone — would go
// on posting a credential the daemon no longer accepts. That is the 2026-09-18
// failure reached by a different road.
//
// (`keld-agent uninstall` is service.Uninstall alone: it removes the
// launchd/systemd/schtasks definition and touches nothing under ~/.keld, so the
// secret is untouched there by construction.)
func TestSignalUninstallLeavesTheTelemetrySecret(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	secret, err := agentcfg.EnsureTelemetrySecret()
	if err != nil {
		t.Fatal(err)
	}
	// Prove the state directory really is removed by this path, so the test is
	// about placement rather than about nothing happening.
	if err := os.MkdirAll(paths.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}

	if err := config.SaveHookConfig("https://atlas.keld.co", "tok"); err != nil {
		t.Fatal(err)
	}
	// One registered tool, removed, so the uninstall reaches the "no tools
	// remain" branch — the one that clears hook.json and the state dir.
	m, _ := buildManifestWithFakeTool(t, home, "claude_code")
	if err := runUninstall(m, nil, true, func(string) bool { return true }); err != nil {
		t.Fatalf("runUninstall: %v", err)
	}

	if _, err := os.Stat(paths.StateDir()); !os.IsNotExist(err) {
		t.Fatal("the state dir survived; this test no longer proves anything")
	}
	after, err := agentcfg.ReadTelemetrySecrets()
	if err != nil {
		t.Fatal(err)
	}
	if after.Secret != secret {
		t.Fatalf("uninstall changed the telemetry secret: %q -> %q", secret, after.Secret)
	}
}
