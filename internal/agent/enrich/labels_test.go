package enrich

import "testing"

func TestSchemaVersion(t *testing.T) {
	if SchemaVersion != 6 {
		t.Fatalf("SchemaVersion = %d, want 6", SchemaVersion)
	}
}

func TestVocabNonEmpty(t *testing.T) {
	if len(TaskTypes) == 0 || len(Domains) == 0 || len(Sensitivity) == 0 {
		t.Fatal("vocab lists must be non-empty")
	}
	if len(SensitiveEntityLabels) == 0 || len(DomainEntityLabels) == 0 {
		t.Fatal("entity label maps must be non-empty")
	}
	if len(SensitivityFromEntity) == 0 {
		t.Fatal("SensitivityFromEntity must be non-empty")
	}
}

func TestSensitivityRuleOrderPHIBeforePII(t *testing.T) {
	// Order matters: ssn -> phi must be evaluated before email -> pii.
	phiIdx, piiIdx := -1, -1
	for i, r := range SensitivityFromEntity {
		if r.Sensitivity == "phi" {
			phiIdx = i
		}
		if r.Sensitivity == "pii" {
			piiIdx = i
		}
	}
	if phiIdx == -1 || piiIdx == -1 || phiIdx > piiIdx {
		t.Fatalf("expected phi rule before pii rule, got phi=%d pii=%d", phiIdx, piiIdx)
	}
}

func TestSubcatsCoverFunctions(t *testing.T) {
	for _, f := range Functions {
		if len(Subcats[f.ID]) == 0 {
			t.Errorf("function %q has no subcategories", f.ID)
		}
	}
	if len(Functions) != 12 {
		t.Fatalf("want 12 functions, got %d", len(Functions))
	}
}
