package enrich

import "testing"

func TestPipelineCollectsCustomResultsAndPreservesBuiltins(t *testing.T) {
	m := &cannedModel{ranked: []Ranked{{Label: "not safe for work", Confidence: 0.7}}}
	p := CustomPass{Key: "nsfw", Kind: "single_label", Title: "NSFW",
		Labels: []CustomLabel{{ID: "safe", Text: "safe for work"}, {ID: "nsfw", Text: "not safe for work"}}}
	w1, w2, _ := BuildCustomExtractors([]CustomPass{p})

	prof := Run("some prompt", "claude_code", Meta{}, m, WithCustomExtractors(w1, w2))

	cr, ok := prof.Custom["nsfw"]
	if !ok || cr.Kind != "single_label" || cr.Value != "nsfw" {
		t.Fatalf("custom result missing/wrong: %+v", prof.Custom)
	}
	// built-in fields still present (unaffected by custom passes)
	if prof.SchemaVersion != SchemaVersion {
		t.Fatalf("built-in schema version clobbered: %d", prof.SchemaVersion)
	}
	// custom extractor version is also recorded in extractor_versions
	if prof.ExtractorVersions["nsfw"] == "" {
		t.Fatalf("expected a version for custom pass nsfw: %+v", prof.ExtractorVersions)
	}
}

func TestPipelineIsolatesFailingCustomPass(t *testing.T) {
	m := &cannedModel{ranked: []Ranked{{Label: "x", Confidence: 1}}}
	bad := panicExtractor{}
	prof := Run("p", "claude_code", Meta{}, m, WithCustomExtractors([]Extractor{bad}, nil))
	if prof.PipelineStatus != "partial" {
		t.Fatalf("expected partial, got %q", prof.PipelineStatus)
	}
	// no panic, failed custom pass contributes nothing
	if len(prof.Custom) != 0 {
		t.Fatalf("failed custom pass should contribute nothing: %+v", prof.Custom)
	}
}

func TestPipelineCustomSingleLabelCarriesAltAndProducer(t *testing.T) {
	m := &cannedModel{ranked: []Ranked{{Label: "not safe for work", Confidence: 0.7}, {Label: "safe for work", Confidence: 0.3}}}
	p := CustomPass{Key: "nsfw", Kind: "single_label", Title: "NSFW", Version: "9",
		Labels: []CustomLabel{{ID: "safe", Text: "safe for work"}, {ID: "nsfw", Text: "not safe for work"}}}
	w1, w2, _ := BuildCustomExtractors([]CustomPass{p})
	prof := Run("x", "claude_code", Meta{}, m, WithCustomExtractors(w1, w2))
	cr := prof.Custom["nsfw"]
	if cr.Value != "nsfw" || len(cr.Alt) != 1 || cr.Alt[0].Value != "safe" {
		t.Fatalf("alt not populated / mapped: %+v", cr)
	}
	if cr.Producer != "nsfw-c9" {
		t.Fatalf("producer = %q, want nsfw-c9", cr.Producer)
	}
}

func TestPipelineCustomEntityCarriesProducer(t *testing.T) {
	p := CustomPass{Key: "contact", Kind: "entity", Title: "Contact", Version: "42",
		Labels: []CustomLabel{{Label: "email", Description: "an email address"}}}
	w1, w2, _ := BuildCustomExtractors([]CustomPass{p})
	prof := Run("mail a@b.com", "claude_code", Meta{}, entityFake{}, WithCustomExtractors(w1, w2))
	cr := prof.Custom["contact"]
	if cr.Kind != "entity" || cr.Producer != "contact-c42" {
		t.Fatalf("entity producer missing/wrong: %+v", cr)
	}
}

type panicExtractor struct{}

func (panicExtractor) Name() string                            { return "boom" }
func (panicExtractor) Version() string                         { return "boom" }
func (panicExtractor) Run(*JobContext) (map[string]any, error) { panic("boom") }
