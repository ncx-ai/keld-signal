package enrich

import "testing"

// phModel returns an api_key entity spanning the whole text (so we can test that a
// placeholder-valued sensitive span is gated out of the secrets classification).
type phModel struct{ emptyModel }

func (phModel) Extract(text string, _ map[string]string, tasks map[string][]string) ExtractResult {
	return ExtractResult{Entities: []Entity{{Label: "api_key", Text: text, Start: 0, End: len(text), Confidence: 1}}}
}

func TestPlaceholderSpanDoesNotTriggerSecrets(t *testing.T) {
	ctx := NewJobContext("YOUR_API_KEY", "claude_code", Meta{}, phModel{})
	out, err := SensitivityExtractor{}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := out["sensitivity"].(Labeled).Value; got == "secrets" {
		t.Fatalf("placeholder YOUR_API_KEY must not classify as secrets; got %s", got)
	}
	if spans := out["sensitivity_spans"].([]Entity); len(spans) != 0 {
		t.Fatalf("placeholder span must be dropped, got %+v", spans)
	}
}

func TestRealSecretStillTriggersSecrets(t *testing.T) {
	ctx := NewJobContext("ghp_16C7e42F292c6912E7710c838347Ae178B4a", "claude_code", Meta{}, phModel{})
	out, err := SensitivityExtractor{}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := out["sensitivity"].(Labeled).Value; got != "secrets" {
		t.Fatalf("real key must classify as secrets; got %s", got)
	}
}
