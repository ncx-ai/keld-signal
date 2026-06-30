package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDefaultsWhenAbsent(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if Load().IncludeEntityText {
		t.Fatal("IncludeEntityText must default to false")
	}
}

func TestLoadReadsIncludeEntityText(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KELD_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "agent-config.json"), []byte(`{"include_entity_text":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if !Load().IncludeEntityText {
		t.Fatal("expected IncludeEntityText=true")
	}
}

func TestLoadInvalidJSONReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KELD_HOME", dir)
	_ = os.WriteFile(filepath.Join(dir, "agent-config.json"), []byte("{not json"), 0o600)
	if Load().IncludeEntityText {
		t.Fatal("invalid JSON must yield defaults")
	}
}
