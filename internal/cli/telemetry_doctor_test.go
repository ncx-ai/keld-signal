package cli

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/teleproxy"
	"github.com/ncx-ai/keld-signal/internal/config"
)

// ⚠️ THROUGH THE REAL WIRING, not a hand-built TelemetryState.
//
// The unit tests in internal/localagent construct the struct directly, and one of
// them asserted a state the wiring could not produce — so it passed while the
// check was structurally inert in production. Anything that decides whether a
// diagnostic can fire at all has to be exercised end to end from what is
// actually on disk.
func telemetryFixture(t *testing.T, hookAge time.Duration, armed bool) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	hook := filepath.Join(home, "hook.json")
	if err := os.WriteFile(hook, []byte(`{"endpoint":"http://x","ingest_token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-hookAge)
	if err := os.Chtimes(hook, at, at); err != nil {
		t.Fatal(err)
	}
	if armed {
		if err := teleproxy.MarkRunning(); err != nil {
			t.Fatal(err)
		}
	}
}

// A proxy that runs but has never forwarded, well past settling, IS the finding.
// This is the population the check exists for and the one it used to miss.
func TestDoctorReportsARunningProxyThatHasNeverForwarded(t *testing.T) {
	telemetryFixture(t, 3*time.Hour, true)
	m := &config.Manifest{Tools: map[string]config.ToolManifest{"claude_code": {}}}

	if p := telemetryState(m).ProblemLine(); p == "" {
		t.Fatal("a machine whose tools were never restarted produced no finding — " +
			"this is exactly the case the check exists for")
	}
}

// No proxy on this machine (direct-push, or a failed bind): unknowable, so silent.
func TestDoctorSaysNothingWithNoProxy(t *testing.T) {
	telemetryFixture(t, 3*time.Hour, false)
	m := &config.Manifest{Tools: map[string]config.ToolManifest{"claude_code": {}}}

	if p := telemetryState(m).ProblemLine(); p != "" {
		t.Fatalf("a machine with no proxy was reported as broken: %q", p)
	}
}

// Freshly set up: armed, but inside the settling window.
func TestDoctorSaysNothingOnAFreshInstall(t *testing.T) {
	telemetryFixture(t, 2*time.Minute, true)
	m := &config.Manifest{Tools: map[string]config.ToolManifest{"claude_code": {}}}

	if p := telemetryState(m).ProblemLine(); p != "" {
		t.Fatalf("a two-minute-old install was reported as broken: %q", p)
	}
}

// No tools configured: nothing to judge.
func TestDoctorSaysNothingWhenNoToolsAreConfigured(t *testing.T) {
	telemetryFixture(t, 3*time.Hour, true)
	if p := telemetryState(&config.Manifest{}).ProblemLine(); p != "" {
		t.Fatalf("an unconfigured machine was reported as broken: %q", p)
	}
}
