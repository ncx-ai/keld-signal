package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// buildManifestWithFakeTool writes a manifest and config file for a fake tool
// and returns the manifest and the config file path.
func buildManifestWithFakeTool(t *testing.T, home, toolName string) (*config.Manifest, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, toolName+".cfg")
	content := "some existing content"
	if err := os.WriteFile(cfgPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	endpoint := "https://ep.example.com"
	actor := "actor1"
	manifest := &config.Manifest{
		Endpoint: &endpoint,
		Actor:    &actor,
		Tools: map[string]config.ToolManifest{
			toolName: {
				Name:       toolName,
				ConfigPath: cfgPath,
				Managed:    map[string]any{},
			},
		},
		Hook: &config.HookRecord{Version: "dev"},
	}
	if err := manifest.Save(); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	return manifest, cfgPath
}

// fakePlan returns a Plan that represents "nothing left after removal".
func fakePlan(name, cfgPath string) tools.Plan {
	return tools.Plan{
		Name:       name,
		ConfigPath: cfgPath,
		AfterText:  "",
		Managed:    map[string]any{},
		Changed:    true,
	}
}

func TestRunUninstallRemovesTool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	// Use a real registered adapter name so tools.Get succeeds.
	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(cfgPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	endpoint := "https://ep.example.com"
	actor := "actor1"
	manifest := &config.Manifest{
		Endpoint: &endpoint,
		Actor:    &actor,
		Tools: map[string]config.ToolManifest{
			"claude_code": {
				Name:       "claude_code",
				ConfigPath: cfgPath,
				Managed:    map[string]any{},
			},
		},
		Hook: &config.HookRecord{Version: "dev"},
	}
	if err := manifest.Save(); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	err := runUninstall(manifest, []string{"claude_code"}, true, func(string) bool { return true })
	if err != nil {
		t.Fatalf("runUninstall error: %v", err)
	}

	if _, ok := manifest.Tools["claude_code"]; ok {
		t.Error("claude_code should have been removed from manifest.Tools")
	}
}

func TestRunUninstallClearsManifestWhenEmpty(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(cfgPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	endpoint := "https://ep.example.com"
	actor := "actor1"
	manifest := &config.Manifest{
		Endpoint: &endpoint,
		Actor:    &actor,
		Tools: map[string]config.ToolManifest{
			"claude_code": {
				Name:       "claude_code",
				ConfigPath: cfgPath,
				Managed:    map[string]any{},
			},
		},
		Hook: &config.HookRecord{Version: "dev"},
	}
	if err := manifest.Save(); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	err := runUninstall(manifest, nil, true, func(string) bool { return true })
	if err != nil {
		t.Fatalf("runUninstall error: %v", err)
	}

	if len(manifest.Tools) != 0 {
		t.Errorf("expected empty tools map, got %v", manifest.Tools)
	}
	if manifest.Endpoint != nil {
		t.Error("endpoint should be cleared when all tools removed")
	}
	if manifest.Actor != nil {
		t.Error("actor should be cleared when all tools removed")
	}
	if manifest.Hook != nil {
		t.Error("hook should be cleared when all tools removed")
	}
}

func TestRunUninstallAbortWhenConfirmFalse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	cfgPath := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(cfgPath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	endpoint := "https://ep.example.com"
	actor := "actor1"
	manifest := &config.Manifest{
		Endpoint: &endpoint,
		Actor:    &actor,
		Tools: map[string]config.ToolManifest{
			"claude_code": {
				Name:       "claude_code",
				ConfigPath: cfgPath,
				Managed:    map[string]any{},
			},
		},
		Hook: &config.HookRecord{Version: "dev"},
	}
	if err := manifest.Save(); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	err := runUninstall(manifest, nil, false, func(string) bool { return false })
	if err != nil {
		t.Fatalf("runUninstall error: %v", err)
	}

	// Manifest should be unmodified.
	if _, ok := manifest.Tools["claude_code"]; !ok {
		t.Error("claude_code should still be in manifest after abort")
	}
	if manifest.Endpoint == nil {
		t.Error("endpoint should still be set after abort")
	}
}
