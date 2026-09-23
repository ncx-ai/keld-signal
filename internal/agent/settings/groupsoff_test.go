// vocab:keep-file — pins that the pre-rename `workstreams_off` key and
// KELD_PROJECTS_FILE are still read.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

func writeAgentConfig(t *testing.T, body string) {
	t.Helper()
	t.Setenv("KELD_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Dir(paths.AgentConfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(paths.AgentConfigPath(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// A machine configured before the rename wrote `workstreams_off`. It must keep
// meaning what it meant: those groups stay switched off.
func TestTheLegacyWorkstreamsOffKeyIsStillRead(t *testing.T) {
	writeAgentConfig(t, `{"workstreams_off": ["marketing"]}`)
	s := Load()
	if !s.GroupOff("marketing") {
		t.Fatalf("a group switched off under the legacy key must still be off: %+v", s.GroupsOff)
	}
}

func TestGroupsOffWinsOverTheLegacyKey(t *testing.T) {
	writeAgentConfig(t, `{"workstreams_off": ["marketing"], "groups_off": ["sales"]}`)
	s := Load()
	if s.GroupOff("marketing") || !s.GroupOff("sales") {
		t.Fatalf("groups_off must win when both keys exist: %+v", s.GroupsOff)
	}
}

func TestWritingGroupsOffDropsTheLegacyKey(t *testing.T) {
	writeAgentConfig(t, `{"workstreams_off": ["marketing"], "blocks": true}`)
	off := []string{"sales"}
	if err := WriteV3Settings(V3Patch{GroupsOff: &off}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(paths.AgentConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Fatal(err)
	}
	if _, ok := raw["workstreams_off"]; ok {
		t.Fatalf("the legacy key must not survive a write, or it could resurface: %s", b)
	}
	var got []string
	if err := json.Unmarshal(raw["groups_off"], &got); err != nil || len(got) != 1 || got[0] != "sales" {
		t.Fatalf("groups_off = %s (%v)", raw["groups_off"], err)
	}
	if string(raw["blocks"]) != "true" {
		t.Fatalf("an unrelated key must survive the write: %s", b)
	}
}

func TestWorkstreamsFileEnvPrefersTheNewName(t *testing.T) {
	t.Setenv(EnvWorkstreamsFile, "/new.json")
	t.Setenv(EnvWorkstreamsFileLegacy, "/old.json")
	if p, name := WorkstreamsFileFromEnv(); p != "/new.json" || name != EnvWorkstreamsFile {
		t.Fatalf("WorkstreamsFileFromEnv = %q, %q", p, name)
	}
}

func TestWorkstreamsFileEnvFallsBackToTheLegacyName(t *testing.T) {
	t.Setenv(EnvWorkstreamsFile, "")
	t.Setenv(EnvWorkstreamsFileLegacy, "/old.json")
	if p, name := WorkstreamsFileFromEnv(); p != "/old.json" || name != EnvWorkstreamsFileLegacy {
		t.Fatalf("WorkstreamsFileFromEnv = %q, %q", p, name)
	}
	if EnvWorkstreamsFile != "KELD_WORKSTREAMS_FILE" || EnvWorkstreamsFileLegacy != "KELD_PROJECTS_FILE" {
		t.Fatalf("env names changed: %s / %s", EnvWorkstreamsFile, EnvWorkstreamsFileLegacy)
	}
}
