package settings

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

func readAtlasEnvConfig(t *testing.T) map[string]any {
	t.Helper()
	data, err := os.ReadFile(paths.AgentConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestWriteAtlasEnvSetsTheKeyAndKeepsEverythingElse(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := os.WriteFile(paths.AgentConfigPath(), []byte(`{"ml_backend":"deterministic","pii_regions":["us"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtlasEnv("dev"); err != nil {
		t.Fatal(err)
	}
	m := readAtlasEnvConfig(t)
	if m["atlas_env"] != "dev" || m["ml_backend"] != "deterministic" || m["pii_regions"] == nil {
		t.Fatalf("after dev: %v", m)
	}
	if err := WriteAtlasEnv("local"); err != nil {
		t.Fatal(err)
	}
	if m := readAtlasEnvConfig(t); m["atlas_env"] != "local" {
		t.Fatalf("after local: %v", m)
	}
	if got := paths.APIBase(); got != "http://localhost:8000" {
		t.Fatalf("APIBase after local = %q", got)
	}
}

func TestWriteAtlasEnvProdRemovesTheKey(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := WriteAtlasEnv("dev"); err != nil {
		t.Fatal(err)
	}
	if err := WriteAtlasEnv("prod"); err != nil {
		t.Fatal(err)
	}
	if _, has := readAtlasEnvConfig(t)["atlas_env"]; has {
		t.Fatal("prod must remove atlas_env, not write it")
	}
}

func TestWriteAtlasEnvRefusesAnUnknownName(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if err := WriteAtlasEnv("staging"); err == nil {
		t.Fatal("staging must be refused")
	}
	if _, err := os.Stat(paths.AgentConfigPath()); !os.IsNotExist(err) {
		t.Fatal("a refused name must write nothing")
	}
}
