package enrich

import (
	"strings"
	"testing"
)

// A stub model that finds NO entities and abstains on sensitivity, so the result
// is driven purely by the deterministic credential detector.
type emptyModel struct{}

func (emptyModel) Classify(string, map[string][]string) map[string][]Ranked { return nil }
func (emptyModel) Entities(string, map[string]string) []Entity              { return nil }
func (emptyModel) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{}
}

func TestSensitivityCatchesCredentialViaDetector(t *testing.T) {
	// GLiNER (stub) finds nothing; the deterministic layer must still flag secrets.
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

// ssnModel returns an ssn entity from Extract so we can verify a credential does
// NOT downgrade a higher-severity phi classification (precedence guard).
type ssnModel struct{ emptyModel }

func (ssnModel) Extract(text string, _ map[string]string, _ map[string][]string) ExtractResult {
	i := strings.Index(text, "123-45-6789")
	if i < 0 {
		return ExtractResult{}
	}
	return ExtractResult{Entities: []Entity{{Label: "ssn", Start: i, End: i + 11, Confidence: 1}}}
}

func TestCredentialDoesNotDowngradePHI(t *testing.T) {
	ctx := NewJobContext("my ssn is 123-45-6789 and key ghp_16C7e42F292c6912E7710c838347Ae178B4a", "claude_code", Meta{}, ssnModel{})
	out, err := SensitivityExtractor{}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := out["sensitivity"].(Labeled).Value; got != "phi" {
		t.Fatalf("sensitivity = %q, want phi (ssn present; a credential must not downgrade it)", got)
	}
}
