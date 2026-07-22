package cli

import (
	"os"
	"path/filepath"
	"strings"
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

// TestRunUninstallWritesGeminiExtraFile is an end-to-end check (through the
// real GeminiAdapter, not a fake) that uninstall's confirm-gated write path
// also commits Plan.ExtraFile: it seeds ~/.gemini/settings.json and
// ~/.gemini/.env as they'd look right after `keld setup` ran for Gemini
// (telemetry block in settings.json, keld OTEL block + a real
// GEMINI_API_KEY in .env), then confirms an uninstall and checks that the
// .env is stripped back down to just GEMINI_API_KEY on disk.
func TestRunUninstallWritesGeminiExtraFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KELD_HOME", t.TempDir())

	a := &tools.GeminiAdapter{}
	p := tools.SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", Actor: "me"}
	cur := "{\n  \"security\": {\n    \"auth\": {\n      \"selectedType\": \"oauth-personal\"\n    }\n  }\n}\n"

	geminiDir := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(geminiDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	envPath := filepath.Join(geminiDir, ".env")
	if err := os.WriteFile(envPath, []byte("GEMINI_API_KEY=real-secret-value\n"), 0o600); err != nil {
		t.Fatalf("seed .env: %v", err)
	}

	applyPlan := a.Apply(&cur, p, false)
	if applyPlan.Conflict != "" {
		t.Fatalf("Apply: unexpected conflict: %s", applyPlan.Conflict)
	}
	// Commit Apply's staged artifacts to disk, simulating a prior confirmed
	// `keld setup` run.
	if err := os.WriteFile(a.ConfigPath(), []byte(applyPlan.AfterText), 0o644); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}
	if applyPlan.ExtraFile == nil {
		t.Fatal("expected Apply's ExtraFile to be non-nil")
	}
	if err := os.WriteFile(applyPlan.ExtraFile.Path, []byte(applyPlan.ExtraFile.AfterText), 0o600); err != nil {
		t.Fatalf("seed .env with keld block: %v", err)
	}

	endpoint := "https://ep.example.com"
	actor := "actor1"
	manifest := &config.Manifest{
		Endpoint: &endpoint,
		Actor:    &actor,
		Tools: map[string]config.ToolManifest{
			"gemini": {
				Name:       "gemini",
				ConfigPath: a.ConfigPath(),
				Managed:    applyPlan.Managed,
			},
		},
		Hook: &config.HookRecord{Version: "dev"},
	}
	if err := manifest.Save(); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	if err := runUninstall(manifest, []string{"gemini"}, true, func(string) bool { return true }); err != nil {
		t.Fatalf("runUninstall error: %v", err)
	}

	got, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("reading .env after uninstall: %v", err)
	}
	envText := string(got)
	if !strings.Contains(envText, "GEMINI_API_KEY=real-secret-value") {
		t.Fatalf("uninstall lost GEMINI_API_KEY from .env:\n%s", envText)
	}
	if strings.Contains(envText, "keld-managed") {
		t.Fatalf("uninstall did not strip keld block from .env:\n%s", envText)
	}
}

// TestRunUninstallDeletesFreshlyCreatedGeminiEnvFile covers the delete branch
// of Plan.ExtraFile: when keld created ~/.gemini/.env fresh (no prior
// GEMINI_API_KEY) and stripping leaves it empty, uninstall's confirm-gated
// write path must delete the file rather than leave an empty husk behind.
func TestRunUninstallDeletesFreshlyCreatedGeminiEnvFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("KELD_HOME", t.TempDir())

	a := &tools.GeminiAdapter{}
	p := tools.SetupParams{Endpoint: "https://atlas.keld.co", IngestToken: "tok", Actor: "me"}

	applyPlan := a.Apply(nil, p, false)
	if applyPlan.Conflict != "" {
		t.Fatalf("Apply: unexpected conflict: %s", applyPlan.Conflict)
	}
	if err := os.MkdirAll(filepath.Dir(a.ConfigPath()), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	if err := os.WriteFile(a.ConfigPath(), []byte(applyPlan.AfterText), 0o644); err != nil {
		t.Fatalf("seed settings.json: %v", err)
	}
	if applyPlan.ExtraFile == nil {
		t.Fatal("expected Apply's ExtraFile to be non-nil")
	}
	envPath := applyPlan.ExtraFile.Path
	if err := os.WriteFile(envPath, []byte(applyPlan.ExtraFile.AfterText), 0o600); err != nil {
		t.Fatalf("seed fresh .env: %v", err)
	}

	endpoint := "https://ep.example.com"
	actor := "actor1"
	manifest := &config.Manifest{
		Endpoint: &endpoint,
		Actor:    &actor,
		Tools: map[string]config.ToolManifest{
			"gemini": {
				Name:       "gemini",
				ConfigPath: a.ConfigPath(),
				Managed:    applyPlan.Managed,
			},
		},
		Hook: &config.HookRecord{Version: "dev"},
	}
	if err := manifest.Save(); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	if err := runUninstall(manifest, []string{"gemini"}, true, func(string) bool { return true }); err != nil {
		t.Fatalf("runUninstall error: %v", err)
	}

	if _, err := os.Stat(envPath); !os.IsNotExist(err) {
		t.Fatalf(".env should have been deleted by uninstall, stat err=%v", err)
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
