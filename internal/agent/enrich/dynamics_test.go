package enrich

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

func b(v bool) *bool       { return &v }
func f(v float64) *float64 { return &v }

// The pass makes ONE /analyze call and publishes both of its halves: what the
// window contains (workstreams) and how it is changing (dynamics). A second
// call would double the cost of the facet for a block the first one already
// computed.
func TestWorkstreamsPassCarriesTheDynamics(t *testing.T) {
	fa := &fakeAnalyze{ok: true, out: WindowAnalysis{
		Workstreams: map[string]Labeled{"branch": {Value: "feat/ledger", Confidence: 0.9}},
		Dynamics: map[string]Dynamic{
			"branch":   {Status: "compared", Reading: "switched", Changed: b(true), Turnover: f(0.4), Decay: f(0.25)},
			"workflow": {Status: "both_absent", Changed: b(false)},
		},
	}}
	ctx := NewJobContext("some prompt", "claude_code", Meta{}, nil) // no Model: the point of this pass
	ctx.TranscriptPath, ctx.PromptID = "/tmp/t.jsonl", "p1"

	got, err := (WorkstreamsExtractor{Analyze: fa.fn}).Run(ctx)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fa.calls != 1 {
		t.Errorf("the analysis was called %d times; dynamics ride the workstreams call", fa.calls)
	}
	dyn, _ := got["dynamics"].(map[string]Dynamic)
	if len(dyn) != 2 {
		t.Fatalf("dynamics not carried: %+v", got)
	}
	if dyn["branch"].Reading != "switched" || dyn["branch"].Turnover == nil || *dyn["branch"].Turnover != 0.4 {
		t.Errorf("branch dynamic mangled: %+v", dyn["branch"])
	}
	if c := dyn["workflow"].Changed; c == nil || *c {
		t.Errorf("both_absent changed = %v, want a pointer to false", c)
	}
}

// An analysis that produced no dynamics (an older sidecar, a window with no
// series) must not put an empty object on the wire: absent and "we compared and
// found nothing" are different facts, the same distinction the workstreams half
// already draws.
func TestWorkstreamsPassOmitsAbsentDynamics(t *testing.T) {
	fa := &fakeAnalyze{ok: true, out: WindowAnalysis{
		Workstreams: map[string]Labeled{"branch": {Value: "main", Confidence: 1}},
	}}
	got, err := (WorkstreamsExtractor{Analyze: fa.fn}).Run(coords(t))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got["dynamics"] != nil {
		t.Errorf("empty dynamics published: %+v", got["dynamics"])
	}
	if ws, _ := got["workstreams"].(map[string]Labeled); ws["branch"].Value != "main" {
		t.Errorf("the digest half must be unaffected: %+v", got)
	}
}

// End-to-end through the pipeline with NO Model at all — ml_backend
// "deterministic". Dynamics ride the same model-free /analyze call the
// workstreams facet does, so they must survive the mode that has no GLiNER2.
func TestRunPublishesDynamicsWithoutAModel(t *testing.T) {
	p := Run("hello", "claude_code", Meta{}, nil,
		WithPassTimeout(0),
		WithCoordinates("/tmp/t.jsonl", "p1"),
		WithWorkstreams(func(path, promptID string, span int) (WindowAnalysis, bool) {
			return WindowAnalysis{
				Workstreams: map[string]Labeled{"branch": {Value: "feat/ledger", Confidence: 1}},
				Dynamics: map[string]Dynamic{
					"branch": {Status: "compared", Reading: "narrowing", ConcentrationShift: f(0.31)},
				},
			}, true
		}))
	if p.Dynamics["branch"].Reading != "narrowing" {
		t.Fatalf("profile missing dynamics: %+v", p.Dynamics)
	}
	if p.Dynamics["branch"].ConcentrationShift == nil || *p.Dynamics["branch"].ConcentrationShift != 0.31 {
		t.Errorf("the number the reading was computed from was dropped: %+v", p.Dynamics["branch"])
	}
	if p.Workstreams["branch"].Value != "feat/ledger" {
		t.Errorf("the digest half regressed: %+v", p.Workstreams)
	}
}

func TestRunWithoutAnAnalyzerPublishesNoDynamics(t *testing.T) {
	p := Run("hello", "claude_code", Meta{}, nil, WithPassTimeout(0))
	if p.Dynamics != nil {
		t.Errorf("want no dynamics, got %+v", p.Dynamics)
	}
}

// Both fields are CLOSED published vocabularies, gated by SchemaVersion, and
// both are computed in Python. A value the sidecar can emit and this package
// does not know is dropped at the conversion boundary
// (sidecar.Client.AnalyzeLabeled), so a drift here does not fail loudly — it
// silently stops publishing a dimension. So the two lists are read from the
// sidecar's own source rather than mirrored by hand.
func TestDynamicsVocabulariesMatchTheSidecar(t *testing.T) {
	src, err := os.ReadFile("../../../sidecar/app/analysis/dynamics.py")
	if err != nil {
		t.Fatalf("cannot read the sidecar's dynamics module, so the Go vocabularies "+
			"are unpinned: %v", err)
	}
	for _, c := range []struct {
		pyName string
		got    []string
	}{{"READINGS", DynamicReadings}, {"STATUSES", DynamicStatuses}} {
		want := pyTuple(t, string(src), c.pyName)
		if len(want) == 0 {
			t.Fatalf("parsed no values out of %s; the comparison would be vacuous", c.pyName)
		}
		if strings.Join(want, ",") != strings.Join(c.got, ",") {
			t.Errorf("%s drifted:\n python %v\n go     %v", c.pyName, want, c.got)
		}
	}
}

// pyTuple extracts the string literals of a module-level `NAME = (...)`
// assignment, in source order (the order is part of the contract for READINGS:
// it is the precedence `reading` applies).
func pyTuple(t *testing.T, src, name string) []string {
	t.Helper()
	// Non-greedy to the FIRST closing paren: the assignments carry trailing
	// per-value comments, so the paren is not the last thing on its line.
	m := regexp.MustCompile(`(?ms)^` + name + ` = \((.*?)\)`).FindStringSubmatch(src)
	if m == nil {
		t.Fatalf("no `%s = (...)` assignment in the sidecar module", name)
	}
	var out []string
	for _, q := range regexp.MustCompile(`"([a-z_]+)"`).FindAllStringSubmatch(m[1], -1) {
		out = append(out, q[1])
	}
	return out
}

func TestKnownDynamicVocabulary(t *testing.T) {
	for _, s := range DynamicStatuses {
		if !KnownDynamicStatus(s) {
			t.Errorf("published status %q not recognised", s)
		}
	}
	for _, r := range DynamicReadings {
		if !KnownDynamicReading(r) {
			t.Errorf("published reading %q not recognised", r)
		}
	}
	// A reading is ABSENT outside `compared` — the empty string is the honest
	// "no conclusion", and it must pass. A status is never absent.
	if !KnownDynamicReading("") {
		t.Error(`"" must be an acceptable reading: none is stated outside "compared"`)
	}
	if KnownDynamicStatus("") {
		t.Error("a dimension with no comparison outcome is not publishable")
	}
	for _, bad := range []string{"consolidating", "slice_sparse", "steady "} {
		if KnownDynamicStatus(bad) || KnownDynamicReading(bad) {
			t.Errorf("%q is not in either published vocabulary", bad)
		}
	}
}
