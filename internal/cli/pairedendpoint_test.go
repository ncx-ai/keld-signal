package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/config"
)

// ⚠️ **THE ENDPOINT STOPPED RIDING THE MANIFEST.** `manifest.json`'s `endpoint`
// is written only by setup's APPLY path, so on every upgrade where no tool
// config needed changing it kept whatever Atlas it last saw. Observed on a real
// machine: paired to localhost:3000 in the manifest while the agent published to
// localhost:8000 from hook.json, with the CLI reporting the first.
func TestTheReportedEndpointComesFromThePairingNotTheManifest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	stale := "http://localhost:3000/v1/ingest"
	m := &config.Manifest{Endpoint: &stale, Tools: map[string]config.ToolManifest{}}
	if err := m.Save(); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	fresh := "http://localhost:8000/v1/ingest"
	if err := config.SaveHookConfig(fresh, "tok"); err != nil {
		t.Fatalf("save hook config: %v", err)
	}

	if got := pairedEndpoint(); got != fresh {
		t.Fatalf("pairedEndpoint() = %q, want the pairing's %q (the manifest still records %q)", got, fresh, stale)
	}
}

// An unpaired machine reports no endpoint rather than a remembered one. It is a
// normal state — the daemon collects and holds until the pairing lands — so
// there must be nothing to report and nothing to be wrong about.
func TestAnUnpairedMachineReportsNoEndpointEvenWithAManifest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	stale := "http://localhost:3000/v1/ingest"
	m := &config.Manifest{Endpoint: &stale, Tools: map[string]config.ToolManifest{}}
	if err := m.Save(); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	if got := pairedEndpoint(); got != "" {
		t.Fatalf("pairedEndpoint() = %q on a machine with no hook.json, want \"\"", got)
	}
}

// The field is still WRITTEN, so an older reader of manifest.json is not broken
// — and it is written on the path that used to leave it behind, so the recorded
// value can no longer disagree with hook.json.
func TestAdoptOnboardingKeepsTheManifestEndpointInStepWithTheHook(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	stale := "http://localhost:3000/v1/ingest"
	m := &config.Manifest{Endpoint: &stale, Tools: map[string]config.ToolManifest{}}
	if err := m.Save(); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	fresh := "https://atlas.example/v1/ingest"
	if err := adoptOnboarding(&api.Onboarding{Endpoint: fresh, IngestToken: "tok"}, func(string) {}); err != nil {
		t.Fatalf("adoptOnboarding: %v", err)
	}

	got, err := config.LoadManifest()
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if got.Endpoint == nil || *got.Endpoint != fresh {
		t.Fatalf("manifest endpoint = %v, want it stamped with the verified onboarding %q", got.Endpoint, fresh)
	}
	if _, err := os.Stat(filepath.Join(home, "hook.json")); err != nil {
		t.Fatalf("hook.json was not written: %v", err)
	}
}
