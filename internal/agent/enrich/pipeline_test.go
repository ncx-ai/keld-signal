package enrich_test

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
)

func TestRunProducesEnrichedProfile(t *testing.T) {
	p := enrich.Run("write a go function; email jane@acme.com", "claude_code", enrich.Meta{}, enrichtest.NewFake(),
		enrich.WithPIIScanner(enrichtest.NewScan()))
	if p.PipelineStatus != "enriched" {
		t.Fatalf("status = %q, want enriched", p.PipelineStatus)
	}
	if p.TaskType.Value != "code_generation" {
		t.Fatalf("task_type = %+v", p.TaskType)
	}
	if p.Sensitivity.Value != "pii" {
		t.Fatalf("sensitivity = %+v, want pii (email)", p.Sensitivity)
	}
	if p.SchemaVersion != enrich.SchemaVersion {
		t.Fatalf("schema version not set")
	}
	// Seven passes since schema v9 dropped speech_act (was eight). The count is
	// the point: a pass that stops running must stop appearing in
	// extractor_versions, or the payload claims work it did not do.
	if len(p.ExtractorVersions) != 7 {
		t.Fatalf("want 7 extractor versions, got %d: %v", len(p.ExtractorVersions), p.ExtractorVersions)
	}
	if _, ok := p.ExtractorVersions["speech_act"]; ok {
		t.Errorf("extractor_versions still names speech_act: %v", p.ExtractorVersions)
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

// TestRunToleratesNilModelAndStillDetectsCredentials pins deterministic-mode
// behavior: the daemon's wireEnrichment hands the pipeline a genuinely nil
// Model (settings.MLBackend == "deterministic"). Every model-dependent pass
// must be skipped cleanly (no panic escaping Run) rather than crash the
// enrichment worker, while SensitivityExtractor's deterministic credential
// layer (pure Go, no model — see CredentialSpans) must still run and produce a
// real signal, since that is the entire point of a deterministic mode.
//
// The status is "enriched", not "partial": a skipped model pass is a pass this
// mode does not have, not a pass that failed. The thinner facet set is stated
// in FacetsSkipped instead — see facets_skipped_test.go.
func TestRunToleratesNilModelAndStillDetectsCredentials(t *testing.T) {
	p := enrich.Run(`export GITHUB_TOKEN="ghp_0123456789abcdefghijklmnopqrstuvwxyzAB"`, "claude_code", enrich.Meta{}, nil)

	if p.PipelineStatus != "enriched" {
		t.Fatalf("status = %q, want enriched (nothing failed; the model passes are absent by design)", p.PipelineStatus)
	}
	if len(p.FacetsSkipped) == 0 {
		t.Fatal("a thinner profile must name what it dropped: FacetsSkipped is empty")
	}
	if p.Sensitivity.Value != "secrets" {
		t.Fatalf("sensitivity = %+v, want secrets from the deterministic credential layer", p.Sensitivity)
	}
	if len(p.SensitivitySpans) == 0 {
		t.Fatal("expected a masked credential span even with a nil Model")
	}
	for _, s := range p.SensitivitySpans {
		if s.Text != "" {
			t.Errorf("raw text must never survive: %q", s.Text)
		}
	}
	// task_type has no model-free path: with a nil Model it must abstain, not panic.
	if p.TaskType.Value != "" || p.TaskType.Confidence != 0 {
		t.Fatalf("task_type needs a model and must abstain, got %+v", p.TaskType)
	}
}

func TestRunIsolatesPanicAsPartial(t *testing.T) {
	// task_type uses Classify (works via embedded Model); sensitivity+domain use
	// Extract (panics). Pipeline must survive and mark partial.
	m := panicModel{Model: enrichtest.NewFake()}
	p := enrich.Run("write a function", "claude_code", enrich.Meta{}, m)
	if p.PipelineStatus != "partial" {
		t.Fatalf("status = %q, want partial", p.PipelineStatus)
	}
	if p.TaskType.Value != "code_generation" {
		t.Fatalf("surviving stage should still populate: %+v", p.TaskType)
	}
}

// countingModel records which passes hit the model. Method-per-pass (confirmed
// against extractors.go, pass.go, a4_compositional.go):
// task_type/activity_type/personal/subcategory all go through
// classifyPass/classifyLabeled → Model.Classify; function_guess also calls
// Classify EXCEPT for a coding-tool source ("claude_code" among them) under the
// default-on A4 compositional override, where it's set structurally with no
// model call at all — gated either way, so the tests below (which only assert
// gated tasks are NOT hit) are unaffected. domain_entities (gated) calls
// Model.Extract. sensitivity (governance, AlwaysRun) calls the model NOT AT ALL
// — its evidence is the gitleaks layer and the PII scan — so it is asserted from
// its output instead. No built-in pass calls Model.Entities any more, so
// entityHits stays 0 by design; the field is kept as the tripwire for one coming
// back.
//
// It used to carry a `speechAct` field so a test could force the gate's model
// half. There is no model half any more: speech_act was dropped at schema v9
// and the gate is the approval lexicon alone (gate.go).
type countingModel struct {
	classifyHits []string // task names passed to Classify
	entityHits   int
	extractHits  int
}

func (m *countingModel) Classify(text string, tasks map[string][]string) map[string][]enrich.Ranked {
	out := map[string][]enrich.Ranked{}
	for task, labels := range tasks {
		m.classifyHits = append(m.classifyHits, task)
		lab := ""
		if len(labels) > 0 {
			lab = labels[0]
		}
		out[task] = []enrich.Ranked{{Label: lab, Confidence: 0.9}}
	}
	return out
}

func (m *countingModel) Entities(text string, labels map[string]string) []enrich.Entity {
	m.entityHits++
	return nil
}
func (m *countingModel) Extract(text string, labels map[string]string, tasks map[string][]string) enrich.ExtractResult {
	m.extractHits++
	return enrich.ExtractResult{}
}

func hit(hits []string, task string) bool {
	for _, h := range hits {
		if h == task {
			return true
		}
	}
	return false
}

func TestGateSkipsSemanticPassesOnPrefilteredTurn(t *testing.T) {
	t.Setenv("KELD_ENRICH_GATE_ENABLED", "1")
	m := &countingModel{}
	p := enrich.Run("ok, do that", "claude_code", enrich.Meta{}, m)
	if p.PipelineStatus != "gated" {
		t.Fatalf("status = %q, want gated", p.PipelineStatus)
	}
	for _, gated := range []string{"task_type", "activity_type", "personal", "function_guess", "subcategory"} {
		if hit(m.classifyHits, gated) {
			t.Errorf("gated pass %q must not hit the model", gated)
		}
	}
	// Governance ran. sensitivity is proved from its OUTPUT, not from a model
	// call: the facet consults no model at all, so a hit counter can no longer
	// witness it. A pass that was skipped commits nothing, so a Producer is the
	// evidence. It is now the ONLY always-run pass — speech_act was the other
	// one, as the gate's model signal, and both the facet and that role are gone
	// at schema v9.
	if p.Sensitivity.Producer == "" {
		t.Errorf("sensitivity (governance) must always run; got %+v", p.Sensitivity)
	}
	// A gated turn must cost NO inference at all now: the pre-filter decides it
	// before any pass that calls the model, and no always-run pass calls one.
	if len(m.classifyHits) != 0 || m.extractHits != 0 || m.entityHits != 0 {
		t.Errorf("gated turn hit the model: classify=%v extract=%d entities=%d",
			m.classifyHits, m.extractHits, m.entityHits)
	}
	// gated semantic fields are empty
	if p.TaskType.Value != "" || p.Activity.Value != "" {
		t.Error("gated turn must leave semantic fields empty")
	}
}

// The gate lost its model branch at schema v9. An input the lexicon does not
// recognise is enriched no matter what the model would have called it — see
// TestGateIsModelFree (speechact_removed_test.go) for the same invariant driven
// from a model that answers "fragment" to everything.
func TestGateDoesNotFireOnANonLexiconTurn(t *testing.T) {
	t.Setenv("KELD_ENRICH_GATE_ENABLED", "1")
	m := &countingModel{}
	// Content-free to a human, but outside the approval lexicon: this is exactly
	// the case the dropped speech_act==fragment branch used to catch. It now
	// costs a full enrichment — wasted compute, never a wrong answer, which is
	// the safe direction for the gate to fail in.
	p := enrich.Run("well, alright then I suppose", "claude_code", enrich.Meta{}, m)
	if p.PipelineStatus == "gated" {
		t.Fatalf("status = %q: the gate must consult no model output", p.PipelineStatus)
	}
	if !hit(m.classifyHits, "task_type") {
		t.Error("task_type must run on an ungated turn")
	}
}

func TestGateRunsAllPassesOnSubstantiveTurn(t *testing.T) {
	t.Setenv("KELD_ENRICH_GATE_ENABLED", "1")
	m := &countingModel{}
	p := enrich.Run("Add a rate limiter to the login endpoint", "claude_code", enrich.Meta{}, m)
	if p.PipelineStatus == "gated" {
		t.Fatal("substantive turn must not be gated")
	}
	if !hit(m.classifyHits, "task_type") {
		t.Error("task_type must run on a substantive turn")
	}
}

func TestGateOffRunsEverything(t *testing.T) {
	t.Setenv("KELD_ENRICH_GATE_ENABLED", "false") // explicitly disabled (default is ON)
	m := &countingModel{}
	p := enrich.Run("ok", "claude_code", enrich.Meta{}, m)
	if p.PipelineStatus == "gated" {
		t.Fatal("gate disabled: nothing should be gated even for 'ok'")
	}
	if !hit(m.classifyHits, "task_type") {
		t.Error("gate disabled: task_type must still run")
	}
}
