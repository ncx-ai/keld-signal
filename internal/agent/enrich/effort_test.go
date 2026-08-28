package enrich

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func i64(v int64) *int64 { return &v }

// The vocabularies are CLOSED published sets computed in Python, and a value
// this binary does not recognise is dropped at the conversion boundary rather
// than failing loudly — so, exactly as the dynamics vocabularies are, they are
// read from the sidecar's own source instead of being mirrored by hand. The
// sidecar is frozen and shipped separately from keld-agent, so an older or newer
// one can sit in ~/.local/bin indefinitely.
func TestEffortVocabulariesMatchTheSidecar(t *testing.T) {
	for _, c := range []struct {
		file   string
		pyName string
		got    []string
	}{
		{"latency.py", "TEMPOS", Tempos},
		{"latency.py", "STATUSES", TempoStatuses},
		{"magnitude.py", "AUTHORED_STATUSES", AuthoredStatuses},
	} {
		src, err := os.ReadFile("../../../sidecar/app/analysis/" + c.file)
		if err != nil {
			t.Fatalf("cannot read %s, so the Go vocabularies are unpinned: %v", c.file, err)
		}
		want := pyTuple(t, string(src), c.pyName)
		if len(want) == 0 {
			t.Fatalf("parsed no values out of %s; the comparison would be vacuous", c.pyName)
		}
		if strings.Join(want, ",") != strings.Join(c.got, ",") {
			t.Errorf("%s drifted:\n python %v\n go     %v", c.pyName, want, c.got)
		}
	}
}

func TestKnownEffortVocabulary(t *testing.T) {
	for _, s := range Tempos {
		if !KnownTempo(s) {
			t.Errorf("published tempo %q not recognised", s)
		}
	}
	for _, s := range TempoStatuses {
		if !KnownTempoStatus(s) {
			t.Errorf("published tempo status %q not recognised", s)
		}
	}
	for _, s := range AuthoredStatuses {
		if !KnownAuthoredStatus(s) {
			t.Errorf("published authored status %q not recognised", s)
		}
	}
	// The empty tempo PASSES: no conclusion is stated outside `attributed`, and
	// that silence is the honest answer rather than a missing one — the same rule
	// KnownDynamicReading follows.
	if !KnownTempo("") {
		t.Error(`an unstated tempo ("") must be publishable: thin/absent state no conclusion`)
	}
	// A status, by contrast, is ALWAYS stated: a block whose status cannot be
	// named is not interpretable.
	if KnownTempoStatus("") || KnownAuthoredStatus("") {
		t.Error("an empty status must not be publishable")
	}
	for _, bad := range []string{"interactive", "fast", "slow", "steady", "no_majority", "tie"} {
		if KnownTempo(bad) {
			t.Errorf("%q is not in the published tempo vocabulary", bad)
		}
	}
	// `thin` is NOT an authored status. A magnitude has no significance floor to
	// fall under (magnitude.authored's own reasoning), so publishing `thin` there
	// would state an abstention that cannot occur.
	if KnownAuthoredStatus("thin") {
		t.Error(`"thin" must not be an authored status: a sum has no evidence floor`)
	}
}

// The pointers are the contract, the same way they are on Dynamic. A plain
// int64/float64 renders "we have no number" as 0 — which for fast_share is
// exactly the measured defect (a one-turn window has zero gaps and 0.0 is what a
// genuinely slow window reports) and for authored_bytes is a claim that nothing
// was authored made on the strength of never having looked.
func TestEffortWithholdsNumbersItDoesNotHaveRatherThanZeroingThem(t *testing.T) {
	e := Effort{AuthoredStatus: "absent", TempoStatus: "absent"}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	got := string(b)
	for _, absent := range []string{"authored_bytes", "fast_share", "tempo"} {
		if strings.Contains(got, `"`+absent+`"`) {
			t.Errorf("%s published with no value behind it: %s", absent, got)
		}
	}
	// The counts and the statuses are always stated, so an abstention is readable
	// rather than merely empty.
	for _, present := range []string{`"authoring_turns":0`, `"gaps":0`,
		`"authored_status":"absent"`, `"tempo_status":"absent"`} {
		if !strings.Contains(got, present) {
			t.Errorf("missing %s: %s", present, got)
		}
	}
}

func TestEffortPublishesTheNumbersItDoesHave(t *testing.T) {
	e := Effort{AuthoredBytes: i64(6520), AuthoringTurns: 3, AuthoredStatus: "attributed",
		FastShare: f(0.542), Gaps: 41, Tempo: "steered", TempoStatus: "attributed"}
	b, _ := json.Marshal(e)
	got := string(b)
	for _, want := range []string{`"authored_bytes":6520`, `"authoring_turns":3`,
		`"fast_share":0.542`, `"gaps":41`, `"tempo":"steered"`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s: %s", want, got)
		}
	}
}

// The pass makes ONE /analyze call and publishes all three of its halves: the
// digest, the dynamics, and the effort block. The effort block needs no
// inference at all — it is counted from timestamps and byte lengths — so it
// survives ml_backend "deterministic" for the same reason the other two do.
func TestWorkstreamsPassCarriesTheEffortBlock(t *testing.T) {
	fa := &fakeAnalyze{ok: true, out: WindowAnalysis{
		Workstreams: map[string]Labeled{"branch": {Value: "feat/ledger", Confidence: 0.9}},
		Effort: &Effort{AuthoredBytes: i64(6520), AuthoringTurns: 3,
			AuthoredStatus: "attributed", FastShare: f(0.83), Gaps: 41,
			Tempo: "steered", TempoStatus: "attributed"},
	}}
	got, err := (WorkstreamsExtractor{Analyze: fa.fn}).Run(coords(t))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if fa.calls != 1 {
		t.Errorf("the analysis was called %d times; effort rides the workstreams call", fa.calls)
	}
	eff, _ := got["effort"].(*Effort)
	if eff == nil {
		t.Fatalf("effort not carried: %+v", got)
	}
	if eff.AuthoredBytes == nil || *eff.AuthoredBytes != 6520 || eff.AuthoringTurns != 3 {
		t.Errorf("the diff magnitude was mangled: %+v", eff)
	}
	if eff.Tempo != "steered" || eff.FastShare == nil || *eff.FastShare != 0.83 {
		t.Errorf("the tempo was mangled: %+v", eff)
	}
}

// An analysis with no effort block (an older sidecar) must not publish a zeroed
// one: every count would read 0 and every status "", which is a real-looking
// answer nobody measured.
func TestWorkstreamsPassOmitsAnAbsentEffortBlock(t *testing.T) {
	fa := &fakeAnalyze{ok: true, out: WindowAnalysis{
		Workstreams: map[string]Labeled{"branch": {Value: "main", Confidence: 1}},
	}}
	got, err := (WorkstreamsExtractor{Analyze: fa.fn}).Run(coords(t))
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if got["effort"] != nil {
		t.Errorf("empty effort published: %+v", got["effort"])
	}
}

// End-to-end with NO Model at all — ml_backend "deterministic".
func TestRunPublishesEffortWithoutAModel(t *testing.T) {
	p := Run("hello", "claude_code", Meta{}, nil,
		WithPassTimeout(0),
		WithCoordinates("/tmp/t.jsonl", "p1"),
		WithWorkstreams(func(path, promptID string, span int, _ ResolvedFacts) (WindowAnalysis, bool) {
			return WindowAnalysis{
				Workstreams: map[string]Labeled{"branch": {Value: "feat/ledger", Confidence: 1}},
				Effort: &Effort{AuthoredBytes: i64(22187), AuthoringTurns: 1,
					AuthoredStatus: "attributed", FastShare: f(0.0), Gaps: 16,
					Tempo: "autonomous", TempoStatus: "attributed"},
			}, true
		}))
	if p.Effort == nil {
		t.Fatalf("profile missing effort: %+v", p)
	}
	if p.Effort.AuthoredBytes == nil || *p.Effort.AuthoredBytes != 22187 {
		t.Errorf("authored bytes dropped: %+v", p.Effort)
	}
	// A single-turn 22 KB authoring is a REAL total, not a thin sample: the gate
	// on a magnitude is count-derived and reported, never a floor on the sum.
	if p.Effort.AuthoringTurns != 1 {
		t.Errorf("authoring turn count dropped: %+v", p.Effort)
	}
	if p.Effort.Tempo != "autonomous" || p.Effort.FastShare == nil || *p.Effort.FastShare != 0.0 {
		t.Errorf("tempo dropped: %+v", p.Effort)
	}
}

func TestRunWithoutAnAnalyzerPublishesNoEffort(t *testing.T) {
	p := Run("hello", "claude_code", Meta{}, nil, WithPassTimeout(0))
	if p.Effort != nil {
		t.Errorf("want no effort, got %+v", p.Effort)
	}
}
