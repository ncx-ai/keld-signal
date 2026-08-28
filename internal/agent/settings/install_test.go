package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

// readConfig decodes the written file into a generic map, so the test sees
// exactly the keys on disk rather than only the ones Settings models.
func readConfig(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(paths.AgentConfigPath())
	if err != nil {
		t.Fatalf("read agent-config.json: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal agent-config.json: %v\n%s", err, data)
	}
	return m
}

func TestWriteInstallDefaultsCreatesBothKeys(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	if err := WriteInstallDefaults("deterministic", true); err != nil {
		t.Fatalf("WriteInstallDefaults: %v", err)
	}
	m := readConfig(t)
	if m["ml_backend"] != "deterministic" {
		t.Errorf("ml_backend = %v, want deterministic", m["ml_backend"])
	}
	if m["blocks"] != true {
		t.Errorf("blocks = %v, want true", m["blocks"])
	}

	// And it round-trips through the real loader, which is what the daemon uses.
	s := Load()
	if s.MLBackend != "deterministic" {
		t.Errorf("Load().MLBackend = %q, want deterministic", s.MLBackend)
	}
	if !s.Blocks {
		t.Error("Load().Blocks = false, want true")
	}
	if s.MLEnabled() {
		t.Error("MLEnabled() = true; deterministic must not enable the model")
	}
}

// The whole reason this is a merge: an operator's settings must outlive an
// installer run. ml_backend has no remote override, so a clobbered pii_regions
// could not be restored from the server either.
func TestWriteInstallDefaultsPreservesOtherKeys(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	existing := `{
  "pii_regions": ["us", "uk"],
  "include_entity_text": true,
  "features": true,
  "unknown_future_key": {"nested": 1}
}`
	if err := os.WriteFile(filepath.Join(home, "agent-config.json"), []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := WriteInstallDefaults("deterministic", true); err != nil {
		t.Fatalf("WriteInstallDefaults: %v", err)
	}

	m := readConfig(t)
	if m["include_entity_text"] != true {
		t.Error("include_entity_text was lost")
	}
	if m["features"] != true {
		t.Error("features was lost")
	}
	// A key no version of Settings models must survive too — the file is the
	// operator's, not this struct's.
	if m["unknown_future_key"] == nil {
		t.Error("unknown_future_key was lost")
	}
	regions, ok := m["pii_regions"].([]any)
	if !ok || len(regions) != 2 || regions[0] != "us" || regions[1] != "uk" {
		t.Errorf("pii_regions = %v, want [us uk]", m["pii_regions"])
	}
	if m["ml_backend"] != "deterministic" || m["blocks"] != true {
		t.Errorf("the two new keys did not land: %v", m)
	}
}

// A re-install must be a no-op on content, not an accumulating rewrite.
func TestWriteInstallDefaultsIsIdempotent(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	if err := WriteInstallDefaults("deterministic", true); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(paths.AgentConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if err := WriteInstallDefaults("deterministic", true); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(paths.AgentConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Errorf("not idempotent:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

// An unreadable/corrupt file must not abort an install, and must not silently
// keep its garbage either: Load() already treats invalid JSON as "defaults", so
// the writer starts from empty rather than failing.
func TestWriteInstallDefaultsOverCorruptFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "agent-config.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteInstallDefaults("deterministic", true); err != nil {
		t.Fatalf("WriteInstallDefaults over corrupt file: %v", err)
	}
	m := readConfig(t)
	if m["ml_backend"] != "deterministic" || m["blocks"] != true {
		t.Errorf("keys did not land over a corrupt file: %v", m)
	}
}

func TestWriteInstallDefaultsRejectsUnknownBackend(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := WriteInstallDefaults("turbo", true); err == nil {
		t.Fatal("want an error for an unknown backend, got nil")
	}
	// And nothing was written: a rejected value must not leave a half-config.
	if _, err := os.Stat(paths.AgentConfigPath()); !os.IsNotExist(err) {
		t.Errorf("a rejected backend wrote a file anyway (stat err = %v)", err)
	}
}

func TestWriteInstallDefaultsFileMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix file modes")
	}
	t.Setenv("KELD_HOME", t.TempDir())
	if err := WriteInstallDefaults("deterministic", true); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(paths.AgentConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", st.Mode().Perm())
	}
}
