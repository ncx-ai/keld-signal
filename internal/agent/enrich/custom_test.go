package enrich

import "testing"

// cannedModel returns fixed Ranked per task and records the text/task-name seen.
// It intentionally does NOT implement MultiLabelModel (see custom_kinds_test.go).
type cannedModel struct {
	lastText string
	lastTask string
	ranked   []Ranked
}

func (m *cannedModel) Classify(text string, tasks map[string][]string) map[string][]Ranked {
	m.lastText = text
	out := map[string][]Ranked{}
	for name := range tasks {
		m.lastTask = name
		out[name] = m.ranked
	}
	return out
}
func (m *cannedModel) Entities(string, map[string]string) []Entity { return nil }
func (m *cannedModel) Extract(t string, l map[string]string, tk map[string][]string) ExtractResult {
	return ExtractResult{Results: m.Classify(t, tk)}
}

func TestCustomSingleLabelUsesTitleAndRawTextMapsToID(t *testing.T) {
	// Model "wins" the readable text "not safe for work" -> id "nsfw".
	m := &cannedModel{ranked: []Ranked{{Label: "not safe for work", Confidence: 0.7}, {Label: "safe for work", Confidence: 0.3}}}
	p := CustomPass{Key: "nsfw", Kind: "single_label", Title: "NSFW",
		Labels: []CustomLabel{{ID: "safe", Text: "safe for work"}, {ID: "nsfw", Text: "not safe for work"}}}
	w1, w2, rej := BuildCustomExtractors([]CustomPass{p})
	if len(w1) != 1 || len(w2) != 0 || len(rej) != 0 {
		t.Fatalf("build: w1=%d w2=%d rej=%v", len(w1), len(w2), rej)
	}
	ctx := NewJobContext("this is porn", "claude_code", Meta{}, m)
	out, err := w1[0].Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.lastTask != "NSFW" { // Lab parity: task name is the TITLE
		t.Fatalf("task name = %q, want NSFW", m.lastTask)
	}
	if m.lastText != "this is porn" { // Lab parity: raw text, no preamble
		t.Fatalf("text = %q, want raw prompt", m.lastText)
	}
	got := out["nsfw"].(Labeled)
	if got.Value != "nsfw" || got.Confidence != 0.7 {
		t.Fatalf("mapped result wrong: %+v", got)
	}
}

func TestCustomSingleLabelLoneValueInjectsHiddenNegative(t *testing.T) {
	// One label; model picks the injected "Not <value>" negative => not tagged.
	m := &cannedModel{ranked: []Ranked{{Label: "Not sensitive topic", Confidence: 0.9}}}
	p := CustomPass{Key: "flag", Kind: "single_label", Title: "Flag",
		Labels: []CustomLabel{{ID: "hit", Text: "sensitive topic"}}}
	w1, _, _ := BuildCustomExtractors([]CustomPass{p})
	ctx := NewJobContext("hello", "claude_code", Meta{}, m)
	out, _ := w1[0].Run(ctx)
	got := out["flag"].(Labeled)
	if got.Value != "" { // negative won -> empty (not tagged), negative stripped
		t.Fatalf("expected empty (not tagged), got %+v", got)
	}
}

func TestCustomConditionedGoesToWave2(t *testing.T) {
	p := CustomPass{Key: "sub", Kind: "single_label", Title: "Sub", ConditionOn: "nsfw",
		LabelsByCond: map[string][]CustomLabel{"nsfw": {{ID: "x", Text: "x kind"}}}}
	w1, w2, rej := BuildCustomExtractors([]CustomPass{p})
	if len(w1) != 0 || len(w2) != 1 || len(rej) != 0 {
		t.Fatalf("conditioned pass should be wave2: w1=%d w2=%d rej=%v", len(w1), len(w2), rej)
	}
}

func TestCustomConditionedSelectsSubsetFromPriorPass(t *testing.T) {
	m := &cannedModel{ranked: []Ranked{{Label: "x kind", Confidence: 0.6}}}
	p := CustomPass{Key: "sub", Kind: "single_label", Title: "Sub", ConditionOn: "nsfw",
		LabelsByCond: map[string][]CustomLabel{"nsfw": {{ID: "x", Text: "x kind"}}}}
	_, w2, _ := BuildCustomExtractors([]CustomPass{p})
	ctx := NewJobContext("p", "claude_code", Meta{}, m)
	ctx.Set("nsfw", map[string]any{"nsfw": Labeled{Value: "nsfw"}}) // prior pass committed "nsfw"
	out, _ := w2[0].Run(ctx)
	if out["sub"].(Labeled).Value != "x" {
		t.Fatalf("conditioned subset not applied: %+v", out["sub"])
	}
}

func TestCustomRejectsBuiltinKeyCollisionAndUnknownKind(t *testing.T) {
	_, _, rej := BuildCustomExtractors([]CustomPass{
		{Key: "task_type", Kind: "single_label", Title: "X", Labels: []CustomLabel{{ID: "a", Text: "a"}}},
		{Key: "weird", Kind: "structure", Title: "W"},
	})
	if len(rej) != 2 {
		t.Fatalf("expected 2 rejects, got %v", rej)
	}
}
