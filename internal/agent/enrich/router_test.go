package enrich

import "testing"

type tagModel struct{ tag string }

func (m tagModel) Classify(string, map[string][]string) map[string][]Ranked {
	return map[string][]Ranked{"task_type": {{Label: m.tag, Confidence: 1}}}
}
func (m tagModel) Entities(string, map[string]string) []Entity { return nil }
func (m tagModel) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{Results: m.Classify("", nil)}
}

func TestRouterPicksByHealth(t *testing.T) {
	healthy := true
	r := NewRouter(tagModel{"sidecar"}, tagModel{"det"}, func() bool { return healthy })
	if got := r.Classify("x", nil)["task_type"][0].Label; got != "sidecar" {
		t.Fatalf("healthy -> want sidecar, got %s", got)
	}
	healthy = false
	if got := r.Classify("x", nil)["task_type"][0].Label; got != "det" {
		t.Fatalf("unhealthy -> want det, got %s", got)
	}
}
