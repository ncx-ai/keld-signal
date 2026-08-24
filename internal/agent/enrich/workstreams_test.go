package enrich

import "testing"

// fakeAnalyze records the coordinates it was called with and returns a canned
// answer, so the pass is exercised without a sidecar.
type fakeAnalyze struct {
	calls  int
	path   string
	prompt string
	span   int
	out    WindowAnalysis
	ok     bool
}

func (f *fakeAnalyze) fn(path, promptID string, spanMinutes int) (WindowAnalysis, bool) {
	f.calls++
	f.path, f.prompt, f.span = path, promptID, spanMinutes
	return f.out, f.ok
}

func TestWorkstreamsPassPopulatesTheProfileWithoutAModel(t *testing.T) {
	f := &fakeAnalyze{out: WindowAnalysis{Workstreams: map[string]Labeled{"project": {Value: "acme", Confidence: 0.83}}}, ok: true}
	ctx := NewJobContext("some prompt", "claude_code", Meta{}, nil) // no Model — the point of this pass
	ctx.TranscriptPath, ctx.PromptID = "/tmp/t.jsonl", "p1"

	got, err := (WorkstreamsExtractor{Analyze: f.fn}).Run(ctx)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	ws, _ := got["workstreams"].(map[string]Labeled)
	if ws["project"].Value != "acme" || ws["project"].Confidence != 0.83 {
		t.Errorf("dimension not carried: %+v", got)
	}
	if ws["project"].Producer != (WorkstreamsExtractor{}).Version() {
		t.Errorf("producer = %q, want %q", ws["project"].Producer, (WorkstreamsExtractor{}).Version())
	}
	if f.path != "/tmp/t.jsonl" || f.prompt != "p1" || f.span != WorkstreamSpanMinutes {
		t.Errorf("coordinates not threaded: path=%q prompt=%q span=%d", f.path, f.prompt, f.span)
	}
}

func TestWorkstreamsPassOmitsTheKeyWhenAnalysisFails(t *testing.T) {
	f := &fakeAnalyze{ok: false}
	got, err := (WorkstreamsExtractor{Analyze: f.fn}).Run(coords(t))
	if err == nil {
		t.Error("a failed analysis must be reported as a failed pass, not a silent empty one")
	}
	if got["workstreams"] != nil {
		t.Error("a failed analysis must not publish an empty workstream set — absent and empty are different facts")
	}
}

// A dimension the window could not attribute (JSON null in /analyze, dropped by
// the converter) must stay ABSENT, never a Labeled with an empty Value: an
// empty value published as a dimension reads downstream as a real answer named
// "".
func TestWorkstreamsPassDropsUnattributedDimensions(t *testing.T) {
	f := &fakeAnalyze{ok: true, out: WindowAnalysis{Workstreams: map[string]Labeled{
		"project": {Value: "acme", Confidence: 1},
		"branch":  {}, // unattributed
	}}}
	got, err := (WorkstreamsExtractor{Analyze: f.fn}).Run(coords(t))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	ws, _ := got["workstreams"].(map[string]Labeled)
	if _, present := ws["branch"]; present {
		t.Errorf("unattributed dimension must be absent, got %+v", ws)
	}
	if len(ws) != 1 {
		t.Errorf("want only the attributed dimension, got %+v", ws)
	}
}

// "The window has no dominant value anywhere" is a real answer, not a failure:
// the pass succeeds (so the profile is not downgraded to "partial") and simply
// contributes no dimensions.
func TestWorkstreamsPassSucceedsWithNoDominantValues(t *testing.T) {
	f := &fakeAnalyze{ok: true, out: WindowAnalysis{Workstreams: map[string]Labeled{"project": {}}}}
	got, err := (WorkstreamsExtractor{Analyze: f.fn}).Run(coords(t))
	if err != nil {
		t.Fatalf("an empty-but-successful analysis must not fail the pass: %v", err)
	}
	if ws, _ := got["workstreams"].(map[string]Labeled); len(ws) != 0 {
		t.Errorf("want no dimensions, got %+v", ws)
	}
}

// Without coordinates there is nothing to analyze: fail fast rather than issue
// a call that can only 404.
func TestWorkstreamsPassNeedsCoordinates(t *testing.T) {
	f := &fakeAnalyze{ok: true, out: WindowAnalysis{Workstreams: map[string]Labeled{"project": {Value: "acme"}}}}
	if _, err := (WorkstreamsExtractor{Analyze: f.fn}).Run(NewJobContext("t", "s", Meta{}, nil)); err == nil {
		t.Error("missing coordinates must fail the pass")
	}
	if f.calls != 0 {
		t.Errorf("analysis called %d times without coordinates", f.calls)
	}
}

func TestWorkstreamsPassIsModelFreeAndAlwaysRuns(t *testing.T) {
	var ex Extractor = WorkstreamsExtractor{}
	mf, ok := ex.(modelFreeExtractor)
	if !ok || !mf.ModelFree() {
		t.Error("the pass needs no Model; gating it on inference readiness defeats its purpose")
	}
	ar, ok := ex.(alwaysRunner)
	if !ok || !ar.AlwaysRun() {
		t.Error("window dimensions are independent of whether the turn itself is contentful")
	}
}

// End-to-end through the pipeline with NO Model at all (deterministic mode):
// the profile must carry the dimensions.
func TestRunPublishesWorkstreamsWithoutAModel(t *testing.T) {
	p := Run("hello", "claude_code", Meta{}, nil,
		WithPassTimeout(0),
		WithCoordinates("/tmp/t.jsonl", "p1"),
		WithWorkstreams(func(path, promptID string, span int) (WindowAnalysis, bool) {
			if path != "/tmp/t.jsonl" || promptID != "p1" {
				t.Errorf("coordinates not threaded into the pipeline: %q %q", path, promptID)
			}
			return WindowAnalysis{Workstreams: map[string]Labeled{"project": {Value: "acme", Confidence: 1}}}, true
		}))
	if p.Workstreams["project"].Value != "acme" {
		t.Fatalf("profile missing workstreams: %+v", p.Workstreams)
	}
	if p.ExtractorVersions["workstreams"] == "" {
		t.Error("the pass must be attributable in extractor_versions")
	}
}

// Callers that never wire an analyzer (eval harness, localagent, tests) must be
// unaffected: no pass, no facet, no downgrade to "partial".
func TestRunWithoutAnAnalyzerRunsNoWorkstreamsPass(t *testing.T) {
	p := Run("hello", "claude_code", Meta{}, nil, WithPassTimeout(0))
	if p.Workstreams != nil {
		t.Errorf("want no workstreams, got %+v", p.Workstreams)
	}
	if _, ran := p.ExtractorVersions["workstreams"]; ran {
		t.Error("the pass must not run when no analyzer is wired")
	}
}

func coords(t *testing.T) *JobContext {
	t.Helper()
	c := NewJobContext("some prompt", "claude_code", Meta{}, nil)
	c.TranscriptPath, c.PromptID = "/tmp/t.jsonl", "p1"
	return c
}

// The analysis resolves a prompt by Claude-Code JSONL shape (a line with
// "type":"user" and a matching "uuid"). Codex ("<session>#<ordinal>") and
// Gemini ("<session>########<ordinal>") prompt ids over their own file shapes
// cannot resolve, so /analyze 404s — which would fail the pass and downgrade
// EVERY Codex/Gemini job to "partial" in the DEFAULT ml_backend mode. The pass
// must not be registered for those sources at all.
func TestRunSkipsWorkstreamsForSourcesTheAnalysisCannotRead(t *testing.T) {
	for _, source := range []string{"codex", "gemini_cli", "other"} {
		called := false
		p := Run("hello", source, Meta{}, nil,
			WithPassTimeout(0),
			WithCoordinates("/tmp/t.jsonl", "sess#3"),
			WithWorkstreams(func(string, string, int) (WindowAnalysis, bool) {
				called = true
				return WindowAnalysis{Workstreams: map[string]Labeled{"project": {Value: "acme", Confidence: 1}}}, true
			}))
		if called {
			t.Errorf("%s: the analysis cannot read this source's transcripts; it must not be called", source)
		}
		if p.Workstreams != nil {
			t.Errorf("%s: unexpected workstreams %+v", source, p.Workstreams)
		}
		if _, ran := p.ExtractorVersions["workstreams"]; ran {
			t.Errorf("%s: the pass must not be registered, so it cannot fail the profile", source)
		}
	}
}

func TestWorkstreamsEligibleSources(t *testing.T) {
	for _, s := range []string{"claude_code", "cowork"} {
		if !WorkstreamsEligible(s) {
			t.Errorf("%s writes Claude-Code-shaped transcripts the analysis reads", s)
		}
	}
	for _, s := range []string{"codex", "gemini_cli", "", "hook"} {
		if WorkstreamsEligible(s) {
			t.Errorf("%s is not analyzable today", s)
		}
	}
}
