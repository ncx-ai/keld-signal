package enrich_test

import (
	"errors"
	"sort"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
)

// credentialPrompt is content-ful (so the gate never fires) and carries a real
// credential, so the deterministic credential layer produces a real signal.
const credentialPrompt = `Please rotate this leaked key before the release: ` +
	`export GITHUB_TOKEN="ghp_0123456789abcdefghijklmnopqrstuvwxyzAB"`

// failingModelFreePass is a pass that needs no model and nevertheless errors —
// a GENUINE failure in deterministic mode, as opposed to a structural skip.
type failingModelFreePass struct{}

func (failingModelFreePass) Name() string    { return "always_fails" }
func (failingModelFreePass) Version() string { return "always_fails-v1" }
func (failingModelFreePass) ModelFree() bool { return true }
func (failingModelFreePass) Run(*enrich.JobContext) (map[string]any, error) {
	return nil, errors.New("boom")
}

// TestDeterministicModeIsEnrichedNotPartial pins the meaning of
// pipeline_status. ml_backend "deterministic" hands Run a nil Model on purpose:
// the model-dependent passes have no such pass in this mode, which is a
// structural absence, not a failure. Every pass that actually ran succeeded, so
// the profile is "enriched" — and the thinner facet set is stated explicitly in
// facets_skipped rather than left to be inferred from missing fields.
func TestDeterministicModeIsEnrichedNotPartial(t *testing.T) {
	p := enrich.Run(credentialPrompt, "claude_code", enrich.Meta{}, nil)

	if p.PipelineStatus != "enriched" {
		t.Fatalf("status = %q, want enriched (nothing failed; the model passes have no pass in this mode)", p.PipelineStatus)
	}
	if p.Sensitivity.Value != "secrets" {
		t.Fatalf("sensitivity = %+v, want secrets from the deterministic credential layer", p.Sensitivity)
	}
	got := append([]string(nil), p.FacetsSkipped...)
	sort.Strings(got)
	want := []string{"activity_type", "domain_entities", "function_guess", "personal", "subcategory", "task_type"}
	if len(got) != len(want) {
		t.Fatalf("facets_skipped = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("facets_skipped = %v, want %v", got, want)
		}
	}
	// The skip must stay visible: a thinner profile must not read as a complete one.
	if p.TaskType.Value != "" {
		t.Fatalf("task_type needs a model and must abstain, got %+v", p.TaskType)
	}
}

// TestDeterministicModeGenuineFailureIsPartial keeps "partial" meaning what it
// says: a pass that should have worked did not. A model-free pass that errors
// is exactly that, even though every model-dependent pass around it is skipped.
func TestDeterministicModeGenuineFailureIsPartial(t *testing.T) {
	p := enrich.Run(credentialPrompt, "claude_code", enrich.Meta{}, nil,
		enrich.WithCustomExtractors([]enrich.Extractor{failingModelFreePass{}}, nil))

	if p.PipelineStatus != "partial" {
		t.Fatalf("status = %q, want partial (a model-free pass genuinely failed)", p.PipelineStatus)
	}
	for _, f := range p.FacetsSkipped {
		if f == "always_fails" {
			t.Fatal("a failed pass must not be reported as skipped")
		}
	}
}

// TestAutoModeFailureStillPartial pins that this change did not weaken the
// signal in the default mode: a real failure with a real Model is still
// "partial", and it is not laundered into facets_skipped.
func TestAutoModeFailureStillPartial(t *testing.T) {
	p := enrich.Run("write a function that parses a config file", "claude_code", enrich.Meta{},
		panicModel{Model: enrichtest.NewFake()})

	if p.PipelineStatus != "partial" {
		t.Fatalf("status = %q, want partial (a pass panicked)", p.PipelineStatus)
	}
	if len(p.FacetsSkipped) != 0 {
		t.Fatalf("facets_skipped = %v, want none: a failure is not a skip", p.FacetsSkipped)
	}
}

// TestAutoModeSuccessReportsNoSkippedFacets: a fully successful auto-mode run
// is unchanged — no skips, and the field is absent from the wire.
func TestAutoModeSuccessReportsNoSkippedFacets(t *testing.T) {
	p := enrich.Run("write a go function; email jane@acme.com", "claude_code", enrich.Meta{}, enrichtest.NewFake())

	if p.PipelineStatus != "enriched" {
		t.Fatalf("status = %q, want enriched", p.PipelineStatus)
	}
	if len(p.FacetsSkipped) != 0 {
		t.Fatalf("facets_skipped = %v, want none", p.FacetsSkipped)
	}
}
