package enrich

import "testing"

// lastLabelModel returns the LAST candidate label as the top pick for every
// task, so function_guess resolves to "gen" (last in Functions) — a non-eng
// pick we can use to prove the compositional override fired (or didn't).
type lastLabelModel struct{}

func (lastLabelModel) Classify(text string, tasks map[string][]string) map[string][]Ranked {
	out := map[string][]Ranked{}
	for name, labels := range tasks {
		if len(labels) > 0 {
			out[name] = []Ranked{{Label: labels[len(labels)-1], Confidence: 1}}
		}
	}
	return out
}
func (lastLabelModel) Entities(string, map[string]string) []Entity { return nil }
func (lastLabelModel) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{}
}

func fnGuess(t *testing.T, tool string, m Model) string {
	t.Helper()
	out, err := (funcGuessExtractor{}).Run(NewJobContext("build the marketing dashboard", tool, Meta{Tool: tool}, m))
	if err != nil {
		t.Fatal(err)
	}
	lbl, _ := out["function_guess"].(Labeled)
	return lbl.Value
}

func TestCompositionalFunction(t *testing.T) {
	// Default (no env set): coding tool forced to eng, model ignored.
	if got := fnGuess(t, "claude_code", lastLabelModel{}); got != "eng" {
		t.Fatalf("A4 default-on must force eng for a coding tool; got %s", got)
	}
	// Default: generic tool stays topical (not forced) → protects false_eng.
	if got := fnGuess(t, "generic", lastLabelModel{}); got != "gen" {
		t.Fatalf("A4 must NOT force eng for a non-coding tool; got %s", got)
	}

	// Explicit disable restores topical (the model's pick) even for a coding tool.
	t.Setenv("KELD_ENRICH_COMPOSITIONAL_FUNCTION", "off")
	if got := fnGuess(t, "claude_code", lastLabelModel{}); got != "gen" {
		t.Fatalf("disable escape-hatch must restore topical (gen); got %s", got)
	}
}

func TestGeminiAsInteractiveCodingTool(t *testing.T) {
	// Gemini should be eligible for context augmentation.
	if !ContextEligible("gemini") {
		t.Errorf("gemini should be eligible for context augmentation")
	}
	// Gemini should be treated as a coding tool with A4 override.
	if got := fnGuess(t, "gemini", lastLabelModel{}); got != "eng" {
		t.Fatalf("A4 must force eng for gemini as a coding tool; got %s", got)
	}
}
