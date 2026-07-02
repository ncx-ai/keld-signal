package enrich

import "testing"

// stubModel returns a fixed top label for a task.
type stubModel struct{ top map[string]string }

func (s stubModel) Classify(_ string, tasks map[string][]string) map[string][]Ranked {
	out := map[string][]Ranked{}
	for task, labels := range tasks {
		want := s.top[task]
		ranked := []Ranked{}
		for i, l := range labels {
			c := 0.4
			if l == want {
				c = 0.95
			}
			ranked = append([]Ranked{{Label: l, Confidence: c}}[:], ranked...)
			_ = i
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
