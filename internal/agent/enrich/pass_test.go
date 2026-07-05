package enrich

import "testing"

// stubModel returns a fixed top label for a task.
type stubModel struct{ top map[string]string }

func (s stubModel) Classify(_ string, tasks map[string][]string) map[string][]Ranked {
	out := map[string][]Ranked{}
	for task, labels := range tasks {
		want := s.top[task]
		ranked := []Ranked{}
		for _, l := range labels {
			c := 0.4
			if l == want {
				c = 0.9
				// When we have a target, put it first with high confidence
				ranked = append([]Ranked{{Label: l, Confidence: c}}, ranked...)
				continue
			}
			ranked = append(ranked, Ranked{Label: l, Confidence: c})
		}
		// If no target was set, at least return the labels in order
		if want == "" && len(ranked) > 0 {
			ranked[0].Confidence = 0.9
		}
		out[task] = ranked
	}
	return out
}
func (s stubModel) Entities(string, map[string]string) []Entity { return nil }
func (s stubModel) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{}
}

func TestClassifyPassMapsReadableToID(t *testing.T) {
	labels := []LabelDef{{"generate", "generating new content"}, {"analyze", "analyzing inputs"}}
	m := stubModel{top: map[string]string{"activity_type": "analyzing inputs"}}
	ctx := NewJobContext("do some analysis", "eval", Meta{}, m)
	top, _ := classifyPass(ctx, "activity_type", labels)
	if top.Value != "analyze" {
		t.Fatalf("want id 'analyze', got %q", top.Value)
	}
}

func TestCondPassExtractorSwitchesLabelSetByFunction(t *testing.T) {
	// Prove that condPassExtractor genuinely switches its label subset based on the
	// function_guess condition, not hardcoded to a single function.

	tests := []struct {
		funcID   string
		wantSubc string // readable text of a subcat under this function
	}{
		{"eng", "debugging and troubleshooting existing code"}, // eng.debug
		{"legal", "contract drafting and review"},              // legal.contract
	}

	for _, tt := range tests {
		t.Run(tt.funcID, func(t *testing.T) {
			// Set up stub to return the specific subcategory text for "subcategory" task
			m := stubModel{
				top: map[string]string{
					"subcategory": tt.wantSubc,
				},
			}
			ctx := NewJobContext("some text", "eval", Meta{}, m)

			// Seed the function_guess result
			ctx.Set("function_guess", map[string]any{
				"function_guess": Labeled{Value: tt.funcID},
			})

			// Run the conditioned pass extractor
			extractor := condPassExtractor{
				Pass{
					Name:         "subcategory",
					ConditionOn:  "function_guess",
					LabelsByCond: Subcats,
				},
			}
			out, err := extractor.Run(ctx)
			if err != nil {
				t.Fatalf("Run failed: %v", err)
			}

			// Extract the subcategory result
			subLabeled, ok := out["subcategory"].(Labeled)
			if !ok {
				t.Fatalf("subcategory is not Labeled: %T", out["subcategory"])
			}

			// Verify the returned ID belongs to the function's subcats
			found := false
			expectedID := ""
			for _, sc := range Subcats[tt.funcID] {
				if sc.Text == tt.wantSubc {
					expectedID = sc.ID
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("subcat text %q not found under function %q", tt.wantSubc, tt.funcID)
			}

			if subLabeled.Value != expectedID {
				t.Fatalf("got subcategory %q, want %q (for function %q)", subLabeled.Value, expectedID, tt.funcID)
			}
		})
	}
}
