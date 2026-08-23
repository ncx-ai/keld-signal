package enrich

import "testing"

// A stub model that finds NO entities and abstains on sensitivity, so the result
// is driven purely by the deterministic credential detector.
type emptyModel struct{}

func (emptyModel) Classify(string, map[string][]string) map[string][]Ranked { return nil }
func (emptyModel) Entities(string, map[string]string) []Entity              { return nil }
func (emptyModel) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{}
}

func TestSensitivityCatchesCredentialViaDetector(t *testing.T) {
	// No PII scan is wired and the model (a stub) is irrelevant to this facet;
	// the pure-Go credential layer must still flag secrets on its own.
	ctx := NewJobContext("here's the token ghp_16C7e42F292c6912E7710c838347Ae178B4a", "claude_code", Meta{}, emptyModel{})
	out, err := SensitivityExtractor{}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := out["sensitivity"].(Labeled).Value; got != "secrets" {
		t.Fatalf("sensitivity = %q, want secrets (from deterministic detector)", got)
	}
	spans := out["sensitivity_spans"].([]Entity)
	if len(spans) == 0 {
		t.Fatal("expected a masked credential span")
	}
	for _, s := range spans {
		if s.Text != "" {
			t.Fatalf("span text must be cleared, got %q", s.Text)
		}
	}
}

// The precedence guard: the credential layer ELEVATES to secrets, it never
// overrides a higher class already present. Both detectors fire here — the scan
// reports the SSN (the synthetic fxSSN, not the textbook 123-45-6789, which the
// well-known gate excludes from every path) and gitleaks reports the token —
// and the rollup must land on phi.
func TestCredentialDoesNotDowngradePHI(t *testing.T) {
	text := "my ssn is " + fxSSN + " and key ghp_16C7e42F292c6912E7710c838347Ae178B4a"
	ctx := NewJobContext(text, "claude_code", Meta{}, nil)
	out, err := SensitivityExtractor{Scan: scanOf([2]string{"ssn", fxSSN})}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := out["sensitivity"].(Labeled).Value; got != "phi" {
		t.Fatalf("sensitivity = %q, want phi (ssn present; a credential must not downgrade it)", got)
	}
	// Premise: both layers really did fire, or the assertion above is vacuous.
	spans := out["sensitivity_spans"].([]Entity)
	labels := map[string]bool{}
	for _, sp := range spans {
		labels[sp.Label] = true
	}
	if !labels["ssn"] || !labels["api_key"] {
		t.Fatalf("spans = %+v, want both an ssn (scan) and an api_key (gitleaks) span", spans)
	}
}
