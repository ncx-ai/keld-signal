package enrich

import "testing"

// describedFake implements Model + the optional DescribedLabelModel and
// MultiLabelModel capabilities, recording which classification path was taken
// and the per-label descriptions it received.
type describedFake struct {
	classifyCalls  int
	describedCalls int
	lastDescTask   DescribedTask
	lastMultiTask  MultiTask
	ranked         []Ranked
}

func (m *describedFake) Classify(text string, tasks map[string][]string) map[string][]Ranked {
	m.classifyCalls++
	out := map[string][]Ranked{}
	for n := range tasks {
		out[n] = m.ranked
	}
	return out
}
func (m *describedFake) Entities(string, map[string]string) []Entity { return nil }
func (m *describedFake) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{}
}
func (m *describedFake) ClassifyDescribed(text string, tasks map[string]DescribedTask) map[string][]Ranked {
	m.describedCalls++
	out := map[string][]Ranked{}
	for n, t := range tasks {
		m.lastDescTask = t
		out[n] = m.ranked
	}
	return out
}
func (m *describedFake) ClassifyMulti(text string, tasks map[string]MultiTask) map[string][]Ranked {
	out := map[string][]Ranked{}
	for n, t := range tasks {
		m.lastMultiTask = t
		out[n] = m.ranked
	}
	return out
}

func TestCustomSingleLabelThreadsValueDescriptionsToDescribedModel(t *testing.T) {
	m := &describedFake{ranked: []Ranked{{Label: "invoice question", Confidence: 0.8}, {Label: "bug report", Confidence: 0.2}}}
	p := CustomPass{Key: "support", Kind: "single_label", Title: "Support topic",
		Labels: []CustomLabel{
			{ID: "billing", Text: "invoice question", Description: "about charges, refunds or payment methods"},
			{ID: "bug", Text: "bug report"},
		}}
	w1, _, _ := BuildCustomExtractors([]CustomPass{p})
	ctx := NewJobContext("why was I charged twice", "claude_code", Meta{}, m)
	if _, err := w1[0].Run(ctx); err != nil {
		t.Fatal(err)
	}
	if m.describedCalls != 1 || m.classifyCalls != 0 {
		t.Fatalf("expected the described path: described=%d classify=%d", m.describedCalls, m.classifyCalls)
	}
	if got := m.lastDescTask.Descriptions["invoice question"]; got != "about charges, refunds or payment methods" {
		t.Fatalf("description not threaded by label text: %q (map=%v)", got, m.lastDescTask.Descriptions)
	}
	if len(m.lastDescTask.Labels) != 2 { // the full readable label set still goes over
		t.Fatalf("labels = %v, want both", m.lastDescTask.Labels)
	}
}

func TestCustomSingleLabelWithoutDescriptionsUsesPlainClassify(t *testing.T) {
	m := &describedFake{ranked: []Ranked{{Label: "a", Confidence: 0.6}, {Label: "b", Confidence: 0.4}}}
	p := CustomPass{Key: "x", Kind: "single_label", Title: "X",
		Labels: []CustomLabel{{ID: "a", Text: "a"}, {ID: "b", Text: "b"}}}
	w1, _, _ := BuildCustomExtractors([]CustomPass{p})
	ctx := NewJobContext("p", "claude_code", Meta{}, m)
	if _, err := w1[0].Run(ctx); err != nil {
		t.Fatal(err)
	}
	if m.classifyCalls != 1 || m.describedCalls != 0 {
		t.Fatalf("no descriptions ⇒ plain Classify expected: classify=%d described=%d", m.classifyCalls, m.describedCalls)
	}
}

func TestCustomMultiLabelThreadsValueDescriptions(t *testing.T) {
	m := &describedFake{ranked: []Ranked{{Label: "pricing", Confidence: 0.9}}}
	p := CustomPass{Key: "topics", Kind: "multi_label", Title: "Topics",
		Labels: []CustomLabel{{ID: "pricing", Text: "pricing", Description: "mentions cost or discounts"}}}
	w1, _, _ := BuildCustomExtractors([]CustomPass{p})
	ctx := NewJobContext("how much does it cost", "claude_code", Meta{}, m)
	if _, err := w1[0].Run(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m.lastMultiTask.Descriptions["pricing"]; got != "mentions cost or discounts" {
		t.Fatalf("multi-label description not threaded: %q (map=%v)", got, m.lastMultiTask.Descriptions)
	}
}
