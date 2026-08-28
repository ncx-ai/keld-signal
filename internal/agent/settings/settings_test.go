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

// --- PII regions -------------------------------------------------------------
//
// Which country tiers of checksum-validated PII recognizers the sidecar runs.
// Shaped like IncludeEntityText — a local base that an Atlas org doc overlays —
// rather than like MLBackend, which is deliberately local and startup-only. The
// difference is that changing regions is a policy decision an org makes for its
// whole fleet, and it needs no process restart to take effect: the value rides
// each /pii request.

func TestPIIRegionsDefaultToUS(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	got := Load().Regions()
	if len(got) != 1 || got[0] != "us" {
		t.Fatalf("Regions() = %v, want [us]", got)
	}
}

func TestPIIRegionsReadFromConfigFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KELD_HOME", dir)
	if err := os.WriteFile(filepath.Join(dir, "agent-config.json"),
		[]byte(`{"pii_regions":["UK"," de ","uk"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Load().Regions()
	// Normalized: lowercased, trimmed, deduped, order preserved. Unknown codes
	// are NOT filtered here — the sidecar owns the list of regions it can
	// actually serve, and duplicating it Go-side would create two lists to keep
	// in step. "de" is exactly such a case: presidio ships no German recognizer.
	if len(got) != 2 || got[0] != "uk" || got[1] != "de" {
		t.Fatalf("Regions() = %v, want [uk de]", got)
	}
}

func TestPIIRegionsEnvOverridesTheFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KELD_HOME", dir)
	_ = os.WriteFile(filepath.Join(dir, "agent-config.json"), []byte(`{"pii_regions":["us"]}`), 0o600)
	t.Setenv("KELD_PII_REGIONS", "au, sg")
	got := Load().Regions()
	if len(got) != 2 || got[0] != "au" || got[1] != "sg" {
		t.Fatalf("Regions() = %v, want [au sg]", got)
	}
}

func TestPIIRegionsEmptyEnvIsNotAnOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("KELD_HOME", dir)
	_ = os.WriteFile(filepath.Join(dir, "agent-config.json"), []byte(`{"pii_regions":["au"]}`), 0o600)
	t.Setenv("KELD_PII_REGIONS", "")
	got := Load().Regions()
	if len(got) != 1 || got[0] != "au" {
		t.Fatalf("Regions() = %v, want [au]", got)
	}
}

func TestPIIRegionsCanBeSetToUniversalOnly(t *testing.T) {
	// An explicit empty list is a real answer — "no country tier at all, just
	// the universal recognizers" — and must survive as empty rather than
	// silently falling back to the `us` default.
	dir := t.TempDir()
	t.Setenv("KELD_HOME", dir)
	_ = os.WriteFile(filepath.Join(dir, "agent-config.json"), []byte(`{"pii_regions":[]}`), 0o600)
	if got := Load().Regions(); got == nil || len(got) != 0 {
		t.Fatalf("Regions() = %v, want an empty (non-nil) slice", got)
	}
	t.Setenv("KELD_PII_REGIONS", "none")
	if got := Load().Regions(); got == nil || len(got) != 0 {
		t.Fatalf("KELD_PII_REGIONS=none: Regions() = %v, want empty", got)
	}
}
