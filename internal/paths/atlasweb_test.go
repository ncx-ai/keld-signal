package paths

import "testing"

func TestAtlasWebBase(t *testing.T) {
	SetAPIBaseOverride("")
	defer SetAPIBaseOverride("")

	t.Setenv("KELD_API_URL", "http://localhost:8000")
	t.Setenv("KELD_ATLAS_WEB_URL", "")
	if got := AtlasWebBase(); got != "http://localhost:8000" {
		t.Fatalf("unset: AtlasWebBase() = %q, want the API base", got)
	}
	t.Setenv("KELD_ATLAS_WEB_URL", "http://localhost:3000/")
	if got := AtlasWebBase(); got != "http://localhost:3000" {
		t.Fatalf("set: AtlasWebBase() = %q, want http://localhost:3000", got)
	}
}
