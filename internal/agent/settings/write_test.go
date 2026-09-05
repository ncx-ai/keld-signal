package settings

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

func TestWriteV3SettingsMergesAndDoesNotDisturbOtherKeys(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	// An operator's file may hold keys this struct does not model at all, and
	// a modelled key it does not touch this call.
	pre := `{"pii_regions":["us","uk"],"ml_backend":"deterministic","blocks":true}`
	if err := os.WriteFile(paths.AgentConfigPath(), []byte(pre), 0o600); err != nil {
		t.Fatal(err)
	}

	off := false
	if err := WriteV3Settings(V3Patch{SendToAtlas: &off}); err != nil {
		t.Fatalf("WriteV3Settings: %v", err)
	}

	data, err := os.ReadFile(paths.AgentConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	var regions []string
	if err := json.Unmarshal(raw["pii_regions"], &regions); err != nil || len(regions) != 2 ||
		regions[0] != "us" || regions[1] != "uk" {
		t.Fatalf("unmodelled key disturbed: %s", raw["pii_regions"])
	}
	if string(raw["ml_backend"]) != `"deterministic"` {
		t.Fatalf("unrelated modelled key disturbed: %s", raw["ml_backend"])
	}
	if string(raw["blocks"]) != "true" {
		t.Fatalf("unrelated modelled key disturbed: %s", raw["blocks"])
	}
	if string(raw["send_to_atlas"]) != "false" {
		t.Fatalf("send_to_atlas not written: %s", raw["send_to_atlas"])
	}

	s := Load()
	if s.SendToAtlas == nil || *s.SendToAtlas != false {
		t.Fatalf("Load() did not observe the write: %+v", s.SendToAtlas)
	}
}

// A PATCH that only sets one v3 key must not rewrite the others back to
// whatever they currently hold — this is what keeps two independent PUTs from
// racing each other's fields.
func TestWriteV3SettingsTouchesOnlyPatchedKeys(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	showBreaks := true
	if err := WriteV3Settings(V3Patch{ShowBreaks: &showBreaks}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(paths.AgentConfigPath())
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	if _, ok := raw["send_to_atlas"]; ok {
		t.Fatalf("send_to_atlas must not appear when the patch never touched it: %s", data)
	}
	if _, ok := raw["dev_blocks"]; ok {
		t.Fatalf("dev_blocks must not appear when the patch never touched it: %s", data)
	}
}

func TestWriteV3SettingsRejectsUnknownDevBlocks(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	bad := "hourly"
	if err := WriteV3Settings(V3Patch{DevBlocks: &bad}); err == nil {
		t.Fatal("want an error for an unknown dev_blocks value")
	}
	if _, err := os.Stat(paths.AgentConfigPath()); err == nil {
		t.Fatal("a rejected write must not touch the file")
	}
}

func TestWriteV3SettingsAcceptsEveryKnownDevBlocksMode(t *testing.T) {
	for _, m := range DevBlocksModes {
		t.Setenv("KELD_HOME", t.TempDir())
		mode := m
		if err := WriteV3Settings(V3Patch{DevBlocks: &mode}); err != nil {
			t.Fatalf("mode %q: %v", m, err)
		}
	}
}

func TestWriteV3SettingsWorkstreamsOffNilBecomesEmptyList(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	var nilSlice []string
	if err := WriteV3Settings(V3Patch{WorkstreamsOff: &nilSlice}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(paths.AgentConfigPath())
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(data, &raw)
	if string(raw["workstreams_off"]) != "[]" {
		t.Fatalf("want an empty array, got %s", raw["workstreams_off"])
	}
}

func TestWriteV3SettingsAttributionAndDevBlocksTogether(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	attr := true
	mode := "prompt"
	if err := WriteV3Settings(V3Patch{Attribution: &attr, DevBlocks: &mode}); err != nil {
		t.Fatal(err)
	}
	s := Load()
	if !s.Attribution {
		t.Fatal("attribution not persisted")
	}
	if s.DevBlocks != "prompt" {
		t.Fatalf("dev_blocks not persisted, got %q", s.DevBlocks)
	}
}

// An absent/corrupt file must not abort the write — same rule
// WriteInstallDefaults follows, and for the same reason: a corrupt config must
// not make the daemon's own settings page unable to fix it.
func TestWriteV3SettingsToleratesMissingAndCorruptFile(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	on := true
	if err := WriteV3Settings(V3Patch{SendToAtlas: &on}); err != nil {
		t.Fatalf("missing file: %v", err)
	}

	t.Setenv("KELD_HOME", t.TempDir())
	if err := os.WriteFile(paths.AgentConfigPath(), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteV3Settings(V3Patch{SendToAtlas: &on}); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}
	s := Load()
	if s.SendToAtlas == nil || !*s.SendToAtlas {
		t.Fatal("write over a corrupt file did not take")
	}
}
