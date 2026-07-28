package enrich

import "testing"

// multiCanned embeds a Model (so it satisfies the base interface) and adds a
// canned ClassifyMulti, recording the threshold it was called with.
type multiCanned struct {
	Model
	ranked map[string][]Ranked
	lastTh map[string]float64
}

func (m *multiCanned) ClassifyMulti(text string, tasks map[string]MultiTask) map[string][]Ranked {
	m.lastTh = map[string]float64{}
	out := map[string][]Ranked{}
	for name, t := range tasks {
		m.lastTh[name] = t.Threshold
		out[name] = m.ranked[name]
	}
	return out
}

func TestCustomMultiLabelEmitsAllTagsAndDefaultsThreshold(t *testing.T) {
	m := &multiCanned{Model: &cannedModel{}, ranked: map[string][]Ranked{
		"Artifact": {{Label: "source code", Confidence: 0.8}, {Label: "docs", Confidence: 0.6}},
	}}
	p := CustomPass{Key: "art", Kind: "multi_label", Title: "Artifact", // ClsThreshold unset
		Labels: []CustomLabel{{ID: "code", Text: "source code"}, {ID: "docs", Text: "docs"}}}
	w1, _, _ := BuildCustomExtractors([]CustomPass{p})
	ctx := NewJobContext("write code", "claude_code", Meta{}, m)
	out, _ := w1[0].Run(ctx)
	vals := out["art"].([]Labeled)
	if len(vals) != 2 || vals[0].Value != "code" || vals[1].Value != "docs" {
		t.Fatalf("multi values wrong: %+v", vals)
	}
	if m.lastTh["Artifact"] != DefaultClsThreshold {
		t.Fatalf("threshold = %v, want default %v", m.lastTh["Artifact"], DefaultClsThreshold)
	}
}

func TestCustomMultiLabelSkippedWhenBackendLacksCapability(t *testing.T) {
	// cannedModel does NOT implement MultiLabelModel -> extractor emits empty, no panic.
	m := &cannedModel{}
	p := CustomPass{Key: "art", Kind: "multi_label", Title: "Artifact",
		Labels: []CustomLabel{{ID: "code", Text: "source code"}}}
	w1, _, _ := BuildCustomExtractors([]CustomPass{p})
	ctx := NewJobContext("x", "claude_code", Meta{}, m)
	out, err := w1[0].Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v := out["art"].([]Labeled); len(v) != 0 {
		t.Fatalf("expected empty when unsupported, got %+v", v)
	}
}

func TestCustomEntityMasksAndDropsText(t *testing.T) {
	m := entityFake{}
	p := CustomPass{Key: "contact", Kind: "entity", Title: "Contact",
		Labels: []CustomLabel{{Label: "email", Description: "an email address"}}}
	w1, _, _ := BuildCustomExtractors([]CustomPass{p})
	ctx := NewJobContext("mail me at a@b.com", "claude_code", Meta{}, m)
	out, _ := w1[0].Run(ctx)
	ents := out["contact"].([]Entity)
	if len(ents) != 1 || ents[0].Text != "" || ents[0].Masked == "" {
		t.Fatalf("entity not masked/text-dropped: %+v", ents)
	}
}

// entityFake returns a single email span (with raw Text) to prove masking.
type entityFake struct{}

func (entityFake) Classify(string, map[string][]string) map[string][]Ranked { return nil }
func (entityFake) Extract(t string, l map[string]string, tk map[string][]string) ExtractResult {
	return ExtractResult{Entities: entityFake{}.Entities(t, l)}
}
func (entityFake) Entities(text string, labels map[string]string) []Entity {
	return []Entity{{Text: "a@b.com", Label: "email", Start: 11, End: 18, Confidence: 0.9}}
}
