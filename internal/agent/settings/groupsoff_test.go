// vocab:keep-file — pins that switched-off groups are stored under 3.0.6's key.
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

// A group 3.0.6 switched off stays off.
func TestAGroupSwitchedOffBy306StaysOff(t *testing.T) {
	writeAgentConfig(t, `{"workstreams_off": ["marketing"]}`)
	if s := Load(); !s.GroupOff("marketing") {
		t.Fatalf("a group switched off by 3.0.6 must still be off: %+v", s.GroupsOff)
	}
}

// ⚠️ LEFT UNTOUCHED by a settings write (Revision 4, 2026-09-25): nothing
// writes the off-list any more, and a machine auto-updated back to 3.0.6 must
// keep the groups a person switched off. So a write of another key merges
// around it, and no second key ever appears beside 3.0.6's.
func TestASettingsWriteLeaves306sOffListAlone(t *testing.T) {
	writeAgentConfig(t, `{"blocks": true, "workstreams_off": ["sales"]}`)
	on := true
	if err := WriteV3Settings(V3Patch{ShowBreaks: &on}); err != nil {
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
	var got []string
	if err := json.Unmarshal(raw["workstreams_off"], &got); err != nil || len(got) != 1 || got[0] != "sales" {
		t.Fatalf("workstreams_off = %s (%v), want [sales] left as it was", raw["workstreams_off"], err)
	}
	if _, ok := raw["groups_off"]; ok {
		t.Fatalf("no second key may appear beside 3.0.6's: %s", b)
	}
	if string(raw["blocks"]) != "true" || string(raw["show_breaks"]) != "true" {
		t.Fatalf("an unrelated key must survive the write and the new one land: %s", b)
	}
}

func TestTheProjectsFileEnvIs306sName(t *testing.T) {
	t.Setenv(EnvProjectsFile, "/p.json")
	if p, name := ProjectsFileFromEnv(); p != "/p.json" || name != "KELD_PROJECTS_FILE" {
		t.Fatalf("ProjectsFileFromEnv = %q, %q", p, name)
	}
}
