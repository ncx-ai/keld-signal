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

func TestMLEnabledDefaultsAuto(t *testing.T) {
	if !(Settings{}).MLEnabled() {
		t.Fatal("empty MLBackend should default to enabled (auto)")
	}
	if !(Settings{MLBackend: "auto"}).MLEnabled() {
		t.Fatal("auto should be enabled")
	}
	if (Settings{MLBackend: "off"}).MLEnabled() {
		t.Fatal("off should be disabled")
	}
}

func TestDeterministicModeEnablesEnrichmentWithoutTheModel(t *testing.T) {
	cases := []struct {
		backend            string
		wantML, wantEnrich bool
	}{
		{"", true, true},
		{"auto", true, true},
		{"deterministic", false, true},
		{"off", false, false},
	}
	for _, c := range cases {
		s := Settings{MLBackend: c.backend}
		if got := s.MLEnabled(); got != c.wantML {
			t.Errorf("%q: MLEnabled=%v want %v", c.backend, got, c.wantML)
		}
		if got := s.EnrichmentEnabled(); got != c.wantEnrich {
			t.Errorf("%q: EnrichmentEnabled=%v want %v", c.backend, got, c.wantEnrich)
		}
	}
}
