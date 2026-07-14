package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// buildManifestWithBackup writes a pristine backup file under
// paths.BackupsDir()/claude_code/settings.json, a current (keld-modified)
// config file, and a manifest recording the backup path.
func buildManifestWithBackup(t *testing.T, pristineContent, currentContent string) (*config.Manifest, string, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(cfgPath, []byte(currentContent), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	backupDir := filepath.Join(paths.BackupsDir(), "claude_code")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		t.Fatalf("mkdir backup dir: %v", err)
	}
	backupPath := filepath.Join(backupDir, "settings.json")
	if err := os.WriteFile(backupPath, []byte(pristineContent), 0o644); err != nil {
		t.Fatalf("write backup: %v", err)
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
				BackupPath: &backupPath,
			},
		},
		Hook: &config.HookRecord{Version: "dev"},
	}
	if err := manifest.Save(); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	return manifest, cfgPath, backupPath
}

// buildManifestWithKeldConfig writes a config file that keld created/modified
// (with an OTEL endpoint + keld hook installed via the real claude_code
// adapter) and a manifest entry with no BackupPath.
func buildManifestWithKeldConfig(t *testing.T) (*config.Manifest, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "settings.json")

	keldConfig := `{
  "env": {
    "OTEL_EXPORTER_OTLP_ENDPOINT": "https://ep.example.com",
    "OTHER_VAR": "keep-me"
  },
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {"type": "command", "command": "keld __hook --source claude_code"}
        ]
      }
    ]
  }
}`
	if err := os.WriteFile(cfgPath, []byte(keldConfig), 0o644); err != nil {
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
				Managed: map[string]any{
					"env_keys":    []string{"OTEL_EXPORTER_OTLP_ENDPOINT"},
					"hook_substr": "keld __hook",
					"created":     false,
				},
				BackupPath: nil,
			},
		},
		Hook: &config.HookRecord{Version: "dev"},
	}
	if err := manifest.Save(); err != nil {
		t.Fatalf("save manifest: %v", err)
	}
	return manifest, cfgPath
}

func TestRunRestoreFromBackup(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	pristine := `{"pristine": true}`
	manifest, cfgPath, _ := buildManifestWithBackup(t, pristine, `{"env": {"OTEL_EXPORTER_OTLP_ENDPOINT": "x"}}`)

	err := runRestore(manifest, nil, true, false, func(string) bool { return true })
	if err != nil {
		t.Fatalf("runRestore error: %v", err)
	}

	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != pristine {
		t.Errorf("config content = %q, want pristine %q", got, pristine)
	}

	if _, ok := manifest.Tools["claude_code"]; ok {
		t.Error("claude_code should have been removed from manifest.Tools")
	}

	// Verify persisted to disk too.
	reloaded, err := config.LoadManifest()
	if err != nil {
		t.Fatalf("reload manifest: %v", err)
	}
	if _, ok := reloaded.Tools["claude_code"]; ok {
		t.Error("claude_code should have been removed from the saved manifest")
	}
}

func TestRunRestoreNoBackupStripsKeldConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	manifest, cfgPath := buildManifestWithKeldConfig(t)

	err := runRestore(manifest, nil, true, false, func(string) bool { return true })
	if err != nil {
		t.Fatalf("runRestore error: %v", err)
	}

	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	text := string(got)
	if strings.Contains(text, "OTEL_EXPORTER_OTLP_ENDPOINT") {
		t.Errorf("config should no longer contain the keld OTEL endpoint, got %q", text)
	}
	if strings.Contains(text, "keld __hook") {
		t.Errorf("config should no longer contain the keld hook, got %q", text)
	}
	if !strings.Contains(text, "keep-me") {
		t.Errorf("config should preserve non-keld content, got %q", text)
	}

	if _, ok := manifest.Tools["claude_code"]; ok {
		t.Error("claude_code should have been removed from manifest.Tools")
	}
}

func TestRunRestoreDryRunWritesNothing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	pristine := `{"pristine": true}`
	current := `{"env": {"OTEL_EXPORTER_OTLP_ENDPOINT": "x"}}`
	manifest, cfgPath, _ := buildManifestWithBackup(t, pristine, current)

	err := runRestore(manifest, nil, false, true, func(string) bool {
		t.Fatal("confirm should not be called in dry-run mode")
		return true
	})
	if err != nil {
		t.Fatalf("runRestore error: %v", err)
	}

	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != current {
		t.Errorf("dry-run should not modify the config; got %q, want unchanged %q", got, current)
	}

	if _, ok := manifest.Tools["claude_code"]; !ok {
		t.Error("dry-run should leave the manifest unchanged (claude_code still present)")
	}

	// Manifest on disk should be untouched (no Save call in dry-run).
	if _, err := os.Stat(paths.ManifestPath()); err == nil {
		reloaded, err := config.LoadManifest()
		if err != nil {
			t.Fatalf("reload manifest: %v", err)
		}
		if _, ok := reloaded.Tools["claude_code"]; !ok {
			t.Error("dry-run should not have saved manifest changes to disk")
		}
	}
}

func TestRunRestoreNamedFiltering(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	dir := t.TempDir()

	// Tool A: has a backup.
	pristineA := `{"a": "pristine"}`
	cfgA := filepath.Join(dir, "a.json")
	if err := os.WriteFile(cfgA, []byte(`{"a": "keld-modified"}`), 0o644); err != nil {
		t.Fatalf("write cfgA: %v", err)
	}
	backupDirA := filepath.Join(paths.BackupsDir(), "claude_code")
	if err := os.MkdirAll(backupDirA, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	backupPathA := filepath.Join(backupDirA, "a.json")
	if err := os.WriteFile(backupPathA, []byte(pristineA), 0o644); err != nil {
		t.Fatalf("write backupA: %v", err)
	}

	// Tool B: also has a backup, but should NOT be touched.
	cfgB := filepath.Join(dir, "b.json")
	currentB := `{"b": "keld-modified"}`
	if err := os.WriteFile(cfgB, []byte(currentB), 0o644); err != nil {
		t.Fatalf("write cfgB: %v", err)
	}
	backupDirB := filepath.Join(paths.BackupsDir(), "codex")
	if err := os.MkdirAll(backupDirB, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	backupPathB := filepath.Join(backupDirB, "b.json")
	if err := os.WriteFile(backupPathB, []byte(`{"b": "pristine"}`), 0o644); err != nil {
		t.Fatalf("write backupB: %v", err)
	}

	endpoint := "https://ep.example.com"
	actor := "actor1"
	manifest := &config.Manifest{
		Endpoint: &endpoint,
		Actor:    &actor,
		Tools: map[string]config.ToolManifest{
			"claude_code": {Name: "claude_code", ConfigPath: cfgA, Managed: map[string]any{}, BackupPath: &backupPathA},
			"codex":       {Name: "codex", ConfigPath: cfgB, Managed: map[string]any{}, BackupPath: &backupPathB},
		},
		Hook: &config.HookRecord{Version: "dev"},
	}
	if err := manifest.Save(); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	err := runRestore(manifest, []string{"claude_code"}, true, false, func(string) bool { return true })
	if err != nil {
		t.Fatalf("runRestore error: %v", err)
	}

	gotA, err := os.ReadFile(cfgA)
	if err != nil {
		t.Fatalf("read cfgA: %v", err)
	}
	if string(gotA) != pristineA {
		t.Errorf("cfgA content = %q, want pristine %q", gotA, pristineA)
	}
	if _, ok := manifest.Tools["claude_code"]; ok {
		t.Error("claude_code should have been removed from manifest.Tools")
	}

	gotB, err := os.ReadFile(cfgB)
	if err != nil {
		t.Fatalf("read cfgB: %v", err)
	}
	if string(gotB) != currentB {
		t.Errorf("cfgB (not named) should be untouched; got %q, want %q", gotB, currentB)
	}
	if _, ok := manifest.Tools["codex"]; !ok {
		t.Error("codex (not named) should still be in manifest.Tools")
	}
}

func TestRunRestoreNothingToRestore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	manifest := &config.Manifest{Tools: map[string]config.ToolManifest{}}

	err := runRestore(manifest, nil, true, false, func(string) bool { return true })
	if err != nil {
		t.Fatalf("runRestore error: %v", err)
	}
}

func TestRunRestoreAbortWhenConfirmFalse(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	pristine := `{"pristine": true}`
	current := `{"env": {"OTEL_EXPORTER_OTLP_ENDPOINT": "x"}}`
	manifest, cfgPath, _ := buildManifestWithBackup(t, pristine, current)

	err := runRestore(manifest, nil, false, false, func(string) bool { return false })
	if err != nil {
		t.Fatalf("runRestore error: %v", err)
	}

	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if string(got) != current {
		t.Errorf("aborted restore should not modify config; got %q, want %q", got, current)
	}
	if _, ok := manifest.Tools["claude_code"]; !ok {
		t.Error("claude_code should still be in manifest after abort")
	}
}
