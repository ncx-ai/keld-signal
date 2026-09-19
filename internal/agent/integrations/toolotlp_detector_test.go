package integrations

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/telemetry"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// detectorWithOTLP is detectorFor plus the Developer switch in a known
// position. The switch is read LIVE per tick, like AutoSetup, so the test can
// move it between ticks the way the page does.
func detectorWithOTLP(on *bool) *Detector {
	d := detectorFor(nil)
	d.Params = func() (tools.SetupParams, error) {
		return tools.SetupParams{
			Endpoint:    "http://127.0.0.1:14318",
			IngestToken: "local-secret",
			BinPath:     "/usr/local/bin/keld",
			ToolOTLP:    *on,
		}, nil
	}
	d.ToolOTLP = func() bool { return *on }
	// ⚠️ TURNING THE SWITCH ON WRITES A CREDENTIAL, so it passes through the
	// same proxy probe every other credential-writing apply does, and the probe
	// is ALWAYS stubbed in a test: one that reached the real loopback port would
	// pass or fail depending on whether the developer's own daemon happens to be
	// up. Turning it OFF removes the block and needs no probe -- see
	// writesACredential.
	d.Probe = func(endpoint, secret string) (telemetry.ProbeOutcome, int) {
		return telemetry.ProbeOK, 200
	}
	return d
}

func codexConfig(t *testing.T, home string) string {
	t.Helper()
	dir := filepath.Join(home, ".codex")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("model = \"gpt-5\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// ⚠️ **A CONFIG WITH THE HOOK AND NO OTEL BLOCK IS CONFIGURED, AND THE DETECTOR
// LEAVES IT ALONE.** This is the pin that stops the default machine being
// rewritten on every poll forever: `Configured`/`ConfiguredOnDisk` ask about the
// hook, and the OTLP lane is a separate fact. Without it the detector would
// re-apply every 60 seconds, taking a backup each time, and doctor would report
// drift on a healthy install.
func TestAConfiguredToolWithNoOTLPBlockIsNotRewritten(t *testing.T) {
	home := isolate(t)
	off := false
	d := detectorWithOTLP(&off)
	path := codexConfig(t, home)

	d.Tick()
	first := readFile(t, path)
	if strings.Contains(first, "[otel]") {
		t.Fatalf("the switch is off and the detector wrote an [otel] table:\n%s", first)
	}
	if !strings.Contains(first, "__hook --source codex") {
		t.Fatalf("the hook was not written; only the OTLP lane is behind the switch:\n%s", first)
	}
	// The drift check the detector uses must agree that nothing is out of step.
	adapter, err := tools.Get("codex")
	if err != nil {
		t.Fatal(err)
	}
	if !tools.ConfiguredOnDisk(adapter, nil) {
		t.Error("a config carrying the hook and no OTEL block reads as NOT configured")
	}

	// Three more polls change nothing at all — byte for byte.
	for i := 0; i < 3; i++ {
		d.Tick()
		if got := readFile(t, path); got != first {
			t.Fatalf("poll %d rewrote a config that already agrees with the switch:\n%s", i+2, got)
		}
	}
}

// Turning the switch ON writes the block on the next pass; turning it OFF again
// takes it back out. Both directions go through ApplyEntry — the one write path
// `keld signal setup` uses — so both leave a backup.
func TestTheDetectorPutsTheOTLPBlockInStepWithTheSwitch(t *testing.T) {
	home := isolate(t)
	on := false
	d := detectorWithOTLP(&on)
	path := codexConfig(t, home)

	d.Tick() // configured with the lane off
	if got := readFile(t, path); strings.Contains(got, "[otel]") {
		t.Fatalf("lane off, but the block is there:\n%s", got)
	}

	on = true
	d.Tick()
	withLane := readFile(t, path)
	if !strings.Contains(withLane, "[otel]") || !strings.Contains(withLane, "127.0.0.1:14318") {
		t.Fatalf("the switch went on and the block was not written:\n%s", withLane)
	}
	if !backupExists(t) {
		t.Error("the config was rewritten without a backup")
	}

	on = false
	d.Tick()
	after := readFile(t, path)
	if strings.Contains(after, "[otel]") {
		t.Fatalf("the switch went off and the block survived:\n%s", after)
	}
	if !strings.Contains(after, "__hook --source codex") {
		t.Fatalf("removing the block took the hook with it:\n%s", after)
	}
	// And it settles: another poll with the switch unchanged rewrites nothing.
	d.Tick()
	if got := readFile(t, path); got != after {
		t.Fatalf("the detector kept rewriting after the config already agreed:\n%s", got)
	}
}

func backupExists(t *testing.T) bool {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(os.Getenv("KELD_HOME"), "backups", "codex"))
	if err != nil {
		return false
	}
	return len(entries) > 0
}
