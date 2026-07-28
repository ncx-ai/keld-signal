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

type panicExtractor struct{}

func (panicExtractor) Name() string                           { return "boom" }
func (panicExtractor) Version() string                        { return "boom" }
func (panicExtractor) Run(*JobContext) (map[string]any, error) { panic("boom") }
