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
	// resolved records the facts the pass forwarded, so a test can assert the
	// daemon's resolution actually reaches /analyze rather than being dropped
	// between the JobContext and the call.
	resolved ResolvedFacts
}

func (f *fakeAnalyze) fn(path, promptID string, spanMinutes int,
	resolved ResolvedFacts) (WindowAnalysis, bool) {
	f.calls++
	f.path, f.prompt, f.span = path, promptID, spanMinutes
	f.resolved = resolved
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

// A dimension the window could not attribute is CARRIED AND LABELLED, not
// dropped. This is the reversal of the rule this test used to pin: the pass used
// to skip any dimension with an empty Value, which threw away the observation
// count along with the value — measured, 924 of 12,016 dimension-slots (7.7%)
// held real evidence and published nothing, 198 of them one observation short of
// the floor.
//
// Nothing is promoted by carrying it. `thin` is still `thin` on the wire, and a
// consumer that reads it as `attributed` is misreporting a fact the payload
// states plainly.
func TestWorkstreamsPassCarriesUnattributedDimensionsWithTheirStatus(t *testing.T) {
	f := &fakeAnalyze{ok: true, out: WindowAnalysis{Workstreams: map[string]Labeled{
		"project": {Value: "acme", Confidence: 1, Evidence: 30, Status: "attributed"},
		"branch":  {Value: "feat/x", Confidence: 1, Evidence: 4, Status: "thin"},
		"skill":   {Status: "absent"},
	}}}
	got, err := (WorkstreamsExtractor{Analyze: f.fn}).Run(coords(t))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	ws, _ := got["workstreams"].(map[string]Labeled)
	if len(ws) != 3 {
		t.Fatalf("every dimension the analysis answered with must be carried, got %+v", ws)
	}
	// The sub-floor dimension: present, with its count, and SAYING it is thin.
	// Value without Status would render as a confident answer off four
	// observations, which is the whole thing the label exists to prevent.
	br := ws["branch"]
	if br.Value != "feat/x" || br.Evidence != 4 || br.Status != "thin" {
		t.Errorf("thin dimension mangled: %+v", br)
	}
	// A level that never fired is still stated, so "we looked and saw nothing"
	// is distinguishable from "we did not look".
	if sk := ws["skill"]; sk.Status != "absent" || sk.Value != "" || sk.Evidence != 0 {
		t.Errorf("absent dimension mangled: %+v", sk)
	}
	if pr := ws["project"]; pr.Status != "attributed" || pr.Evidence != 30 {
		t.Errorf("attributed dimension mangled: %+v", pr)
	}
	for dim, l := range ws {
		if l.Producer != (WorkstreamsExtractor{}).Version() {
			t.Errorf("%s lost its producer stamp: %+v", dim, l)
		}
	}
}

// "The window has no dominant value anywhere" is a real answer, not a failure:
// the pass succeeds (so the profile is not downgraded to "partial") and now says
// so per dimension rather than contributing nothing at all. A blank row that
// still publishes is deliberate — a suppressed row reads as an oversight, and an
// oversight is what someone eventually "fixes".
func TestWorkstreamsPassSucceedsWithNoDominantValues(t *testing.T) {
	f := &fakeAnalyze{ok: true, out: WindowAnalysis{
		Workstreams: map[string]Labeled{"project": {Status: "absent"}}}}
	got, err := (WorkstreamsExtractor{Analyze: f.fn}).Run(coords(t))
	if err != nil {
		t.Fatalf("an empty-but-successful analysis must not fail the pass: %v", err)
	}
	ws, _ := got["workstreams"].(map[string]Labeled)
	if len(ws) != 1 || ws["project"].Status != "absent" || ws["project"].Value != "" {
		t.Errorf("want one dimension stating its absence, got %+v", ws)
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
		WithWorkstreams(func(path, promptID string, span int, _ ResolvedFacts) (WindowAnalysis, bool) {
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
			WithWorkstreams(func(string, string, int, ResolvedFacts) (WindowAnalysis, bool) {
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

// THE POINT OF THE WHOLE CHANGE, asserted at the seam: the facts the daemon
// resolved reach the analyzer. Before this they were resolved and spent on a
// GLiNER2 prompt STRING (Meta.PreambleCoding()), and enrich.Meta never reaches
// publish.Enrichment — so the analysis was blind to them and the payload named
// the repository from the workspace directory basename instead.
func TestTheWorkstreamsPassForwardsTheDaemonsResolvedFacts(t *testing.T) {
	want := ResolvedFacts{
		Repo:      "github.com/ncx-ai/keld-atlas",
		GitBranch: "feat/ledger",
		Project:   "keld",
	}
	f := &fakeAnalyze{ok: true, out: WindowAnalysis{
		Workstreams: map[string]Labeled{"repo": {Value: want.Repo, Confidence: 1}}}}
	ctx := NewJobContext("some prompt", "claude_code", Meta{}, nil)
	ctx.TranscriptPath, ctx.PromptID = "/tmp/t.jsonl", "p1"
	ctx.Resolved = want

	if _, err := (WorkstreamsExtractor{Analyze: f.fn}).Run(ctx); err != nil {
		t.Fatalf("err = %v", err)
	}
	if f.resolved != want {
		t.Errorf("analyzer received %+v, want %+v — the resolution was dropped between the "+
			"JobContext and the call", f.resolved, want)
	}
}

// Threaded through the pipeline OPTION, not only settable on a hand-built
// context: the daemon's only way in is enrich.Run(..., WithResolvedFacts(...)),
// so that is the path worth pinning.
func TestWithResolvedFactsReachesTheAnalyzerThroughRun(t *testing.T) {
	var got ResolvedFacts
	want := ResolvedFacts{Repo: "gitlab.com/team/sub/proj", GitBranch: "main"}
	Run("hello", "claude_code", Meta{}, nil,
		WithPassTimeout(0),
		WithCoordinates("/tmp/t.jsonl", "p1"),
		WithResolvedFacts(want),
		WithWorkstreams(func(_, _ string, _ int, r ResolvedFacts) (WindowAnalysis, bool) {
			got = r
			return WindowAnalysis{Workstreams: map[string]Labeled{
				"repo": {Value: r.Repo, Confidence: 1}}}, true
		}))
	if got != want {
		t.Errorf("analyzer received %+v, want %+v", got, want)
	}
}

// A caller with no cwd — the eval harness, inline text, localagent — omits the
// option, and the pass must still RUN. The zero value is a normal answer ("this
// directory is not a checkout"), not a reason to withhold the analysis: the
// sidecar writes no repository rows for it and every other dimension is
// unaffected.
func TestNoResolvedFactsStillRunsTheAnalysis(t *testing.T) {
	calls := 0
	p := Run("hello", "claude_code", Meta{}, nil,
		WithPassTimeout(0),
		WithCoordinates("/tmp/t.jsonl", "p1"),
		WithWorkstreams(func(_, _ string, _ int, r ResolvedFacts) (WindowAnalysis, bool) {
			calls++
			if !r.Zero() {
				t.Errorf("expected the zero value, got %+v", r)
			}
			return WindowAnalysis{Workstreams: map[string]Labeled{
				"branch": {Value: "main", Confidence: 1}}}, true
		}))
	if calls != 1 {
		t.Fatalf("the analysis ran %d times; an unresolved checkout must not skip it", calls)
	}
	if p.Workstreams["branch"].Value != "main" {
		t.Errorf("the other dimensions must be unaffected: %+v", p.Workstreams)
	}
}

// Zero() is what decides whether the sidecar sees `resolved: null` (its
// back-compat path) or an object, so its boundary is worth stating: ANY field
// set means "something was resolved".
func TestResolvedFactsZeroIsAllThreeFieldsEmpty(t *testing.T) {
	if !(ResolvedFacts{}).Zero() {
		t.Error("the zero value must report Zero")
	}
	for _, r := range []ResolvedFacts{
		{Repo: "github.com/o/r"}, {GitBranch: "main"}, {Project: "keld"},
	} {
		if r.Zero() {
			t.Errorf("%+v reported Zero; any resolved field must send an object", r)
		}
	}
}
