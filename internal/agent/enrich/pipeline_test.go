package enrich_test

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
)

func TestRunProducesEnrichedProfile(t *testing.T) {
	p := enrich.Run("write a go function; email jane@acme.com", "claude_code", enrich.Meta{}, enrichtest.NewFake())
	if p.PipelineStatus != "enriched" {
		t.Fatalf("status = %q, want enriched", p.PipelineStatus)
	}
	if p.TaskType.Value != "codegen" {
		t.Fatalf("task_type = %+v", p.TaskType)
	}
	if p.Sensitivity.Value != "pii" {
		t.Fatalf("sensitivity = %+v, want pii (email)", p.Sensitivity)
	}
	if p.SchemaVersion != enrich.SchemaVersion {
		t.Fatalf("schema version not set")
	}
	if len(p.ExtractorVersions) != 7 {
		t.Fatalf("want 7 extractor versions, got %d", len(p.ExtractorVersions))
	}
	if p.EnrichedAt.IsZero() {
		t.Fatal("EnrichedAt must be set")
	}
}

func TestProfileHasActivityAndFunctionGuess(t *testing.T) {
	// The fake backend has no keyword priors for these job-category facets, so
	// it must abstain (empty value, zero confidence) rather than emit a
	// meaningless fallback label. Atlas gates on this emptiness; see
	// TestCondPassExtractorSwitchesLabelSetByFunction and
	// TestClassifyPassMapsReadableToID for real-label wiring via stub models.
	p := enrich.Run("write a python function to sort a list", "eval", enrich.Meta{}, enrichtest.NewFake())
	if p.Activity.Value != "" || p.Activity.Confidence != 0 {
		t.Errorf("expected activity_type to abstain, got %+v", p.Activity)
	}
	if p.FunctionGuess.Value != "" || p.FunctionGuess.Confidence != 0 {
		t.Errorf("expected function_guess to abstain, got %+v", p.FunctionGuess)
	}
	if p.Personal.Value != "" || p.Personal.Confidence != 0 {
		t.Errorf("expected personal to abstain, got %+v", p.Personal)
	}
}

func TestSubcategoryConditionsOnFunctionGuess(t *testing.T) {
	// The fake backend abstains on function_guess (no keyword priors), so
	// subcategory's conditioning on it must cascade the abstention rather than
	// pick an arbitrary label set. Real conditioning behavior (subcategory id
	// belonging to the guessed function) is covered by
	// TestCondPassExtractorSwitchesLabelSetByFunction via a stub model.
	p := enrich.Run("debug why this handler throws a 500 error", "eval", enrich.Meta{}, enrichtest.NewFake())
	if p.FunctionGuess.Value != "" {
		t.Fatalf("expected function_guess to abstain, got %+v", p.FunctionGuess)
	}
	if p.Subcategory.Value != "" || p.Subcategory.Confidence != 0 {
		t.Fatalf("expected subcategory to abstain when function_guess abstains, got %+v", p.Subcategory)
	}
}

type panicModel struct{ enrich.Model }

func (panicModel) Extract(string, map[string]string, map[string][]string) enrich.ExtractResult {
	panic("boom")
}

func TestRunIsolatesPanicAsPartial(t *testing.T) {
	// task_type uses Classify (works via embedded Model); sensitivity+domain use
	// Extract (panics). Pipeline must survive and mark partial.
	m := panicModel{Model: enrichtest.NewFake()}
	p := enrich.Run("write a function", "claude_code", enrich.Meta{}, m)
	if p.PipelineStatus != "partial" {
		t.Fatalf("status = %q, want partial", p.PipelineStatus)
	}
	if p.TaskType.Value != "codegen" {
		t.Fatalf("surviving stage should still populate: %+v", p.TaskType)
	}
}
