package enrich

import "testing"

func TestRunProducesEnrichedProfile(t *testing.T) {
	p := Run("write a go function; email jane@acme.com", "claude_code", Meta{}, NewDeterministic())
	if p.PipelineStatus != "enriched" {
		t.Fatalf("status = %q, want enriched", p.PipelineStatus)
	}
	if p.TaskType.Value != "codegen" {
		t.Fatalf("task_type = %+v", p.TaskType)
	}
	if p.Sensitivity.Value != "pii" {
		t.Fatalf("sensitivity = %+v, want pii (email)", p.Sensitivity)
	}
	if p.SchemaVersion != SchemaVersion {
		t.Fatalf("schema version not set")
	}
	if len(p.ExtractorVersions) != 6 {
		t.Fatalf("want 6 extractor versions, got %d", len(p.ExtractorVersions))
	}
	if p.EnrichedAt.IsZero() {
		t.Fatal("EnrichedAt must be set")
	}
}

func TestProfileHasActivityAndFunctionGuess(t *testing.T) {
	p := Run("write a python function to sort a list", "eval", Meta{}, NewDeterministic())
	if p.Activity.Value == "" {
		t.Error("expected an activity_type")
	}
	if p.FunctionGuess.Value == "" {
		t.Error("expected a function_guess")
	}
	if p.Personal.Value == "" {
		t.Error("expected a personal label")
	}
}

func TestSubcategoryConditionsOnFunctionGuess(t *testing.T) {
	// deterministic backend keys on "debug" -> eng.debug once function=eng.
	p := Run("debug why this handler throws a 500 error", "eval", Meta{}, NewDeterministic())
	if p.FunctionGuess.Value == "" {
		t.Fatal("no function guess")
	}
	// subcategory id must belong to the guessed function
	subs := Subcats[p.FunctionGuess.Value]
	ok := false
	for _, s := range subs {
		if s.ID == p.Subcategory.Value {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("subcategory %q not under function %q", p.Subcategory.Value, p.FunctionGuess.Value)
	}
}

type panicModel struct{ Model }

func (panicModel) Extract(string, map[string]string, map[string][]string) ExtractResult {
	panic("boom")
}

func TestRunIsolatesPanicAsPartial(t *testing.T) {
	// task_type uses Classify (works via embedded Model); sensitivity+domain use
	// Extract (panics). Pipeline must survive and mark partial.
	m := panicModel{Model: NewDeterministic()}
	p := Run("write a function", "claude_code", Meta{}, m)
	if p.PipelineStatus != "partial" {
		t.Fatalf("status = %q, want partial", p.PipelineStatus)
	}
	if p.TaskType.Value != "codegen" {
		t.Fatalf("surviving stage should still populate: %+v", p.TaskType)
	}
}
