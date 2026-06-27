// internal/config/merge_toml_test.go
package config

import (
	"strings"
	"testing"
)

func TestUpsertAndStripRoundtrip(t *testing.T) {
	body := "[otel]\nenvironment = \"prod\"\n"
	out := UpsertKeldBlock("", body)
	if !HasKeldBlock(out) {
		t.Fatal("expected block present")
	}
	if err := ValidateTOML(out); err != nil {
		t.Fatalf("invalid toml: %v", err)
	}
	stripped := StripKeldBlock(out)
	if stripped != "" {
		t.Fatalf("expected empty after strip, got %q", stripped)
	}
}

func TestUpsertKeepsUserContent(t *testing.T) {
	existing := "[model]\nname = \"x\"\n"
	out := UpsertKeldBlock(existing, "[otel]\na = 1\n")
	if !HasKeldBlock(out) || !strings.Contains(out, "name = \"x\"") {
		t.Fatalf("must keep user content + block:\n%s", out)
	}
	// re-upsert replaces the prior block, not duplicates it
	out2 := UpsertKeldBlock(out, "[otel]\nb = 2\n")
	if strings.Count(out2, KeldTOMLStart) != 1 {
		t.Fatalf("expected single block after re-upsert:\n%s", out2)
	}
}

func TestStripTOMLTableDropsOnlyTable(t *testing.T) {
	in := "[keep]\nx=1\n[otel]\ny=2\n[otel.sub]\nz=3\n[after]\nw=4\n"
	out := StripTOMLTable(in, "otel")
	if strings.Contains(out, "[otel]") || strings.Contains(out, "[otel.sub]") {
		t.Fatalf("otel not fully stripped:\n%s", out)
	}
	if !strings.Contains(out, "[keep]") || !strings.Contains(out, "[after]") {
		t.Fatalf("dropped unrelated tables:\n%s", out)
	}
}
