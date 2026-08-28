package agentcli

import (
	"strings"
	"testing"
)

// A facet with no labelled gold rows must PRINT as unscored. eval.Score omits
// `accuracy` for an empty denominator, so a formatter that reads the map
// blindly would print `accuracy=0.000` — a real-looking regression where the
// truth is "nothing was measured". Both readings are wrong for a human table;
// only naming it is right.
func TestFacetLineNamesAnUnscoredFacet(t *testing.T) {
	got := facetLine("personal", map[string]float64{"considered": 0})
	if strings.Contains(got, "accuracy") {
		t.Fatalf("unscored facet printed an accuracy: %q", got)
	}
	if !strings.Contains(got, "unscored") || !strings.Contains(got, "personal") {
		t.Fatalf("line = %q, want it to name the facet and say unscored", got)
	}
}

func TestFacetLineCarriesTheDenominator(t *testing.T) {
	got := facetLine("task_type", map[string]float64{"considered": 161, "accuracy": 0.733})
	for _, want := range []string{"task_type", "0.733", "161"} {
		if !strings.Contains(got, want) {
			t.Fatalf("line = %q, missing %q", got, want)
		}
	}
}

// sensitive_recall gets the same treatment: an empty denominator prints as
// unscored, never as 1.000 or 0.000.
func TestSensitiveRecallLineNamesAnEmptyDenominator(t *testing.T) {
	got := facetLine("sensitivity", map[string]float64{"considered": 160, "accuracy": 0.994, "sensitive_considered": 0})
	if strings.Contains(got, "sensitive_recall=") {
		t.Fatalf("empty-denominator recall printed a value: %q", got)
	}
	if !strings.Contains(got, "sensitive_recall unscored") {
		t.Fatalf("line = %q, want it to name sensitive_recall as unscored", got)
	}
	full := facetLine("sensitivity", map[string]float64{"considered": 160, "accuracy": 0.994, "sensitive_considered": 16, "sensitive_recall": 1.0})
	if !strings.Contains(full, "sensitive_recall=1.000") || !strings.Contains(full, "16") {
		t.Fatalf("line = %q, want recall + its denominator", full)
	}
}
