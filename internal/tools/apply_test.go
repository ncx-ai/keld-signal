package tools

import (
	"os"
	"path/filepath"
	"testing"
)

// The drift check answers from the FILE, not from the manifest that claims the
// file was written. That is the whole point of extracting it: doctor and the
// integrations pane must ask one question of one file.
func TestConfiguredOnDiskReadsTheFileBackNotTheManifest(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)

	a := &ClaudeAdapter{}
	managed := map[string]any{"hook_substr": "keld __hook"}

	// No file at all: not configured, whatever the manifest says.
	if ConfiguredOnDisk(a, managed) {
		t.Fatal("ConfiguredOnDisk = true with no config file on disk")
	}

	// A file that is real but holds none of keld's blocks: still not configured.
	if err := os.MkdirAll(filepath.Dir(a.ConfigPath()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.ConfigPath(), []byte(`{"env":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if ConfiguredOnDisk(a, managed) {
		t.Fatal("ConfiguredOnDisk = true for a config holding none of keld's blocks")
	}
}

func TestReadConfigIsNilWhenAbsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	if got := ReadConfig(&ClaudeAdapter{}); got != nil {
		t.Fatalf("ReadConfig = %q, want nil for an absent file", *got)
	}
}
