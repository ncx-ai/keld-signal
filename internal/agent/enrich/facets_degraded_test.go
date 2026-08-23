package enrich_test

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
)

// ssnPrompt carries concrete PII (an SSN) and NO credential pattern, so the
// deterministic credential layer finds nothing and only the model's NER half
// could ever have found this. It is content-ful so the gate never fires.
const ssnPrompt = `Please update the customer record for Dana Rivers, ` +
	`social security number 123-45-6789, in the billing export.`

// degradablePass is an arbitrary (non-sensitivity) pass that runs with reduced
// capability, proving the declaration is a general Extractor capability and not
// a special case wired into sensitivity.
type degradablePass struct{ degraded bool }

func (degradablePass) Name() string                       { return "half_blind" }
func (degradablePass) Version() string                    { return "half_blind-v1" }
func (degradablePass) ModelFree() bool                    { return true }
func (p degradablePass) Degraded(*enrich.JobContext) bool { return p.degraded }
func (degradablePass) Run(*enrich.JobContext) (map[string]any, error) {
	return map[string]any{"half_blind": enrich.Labeled{Value: "x"}}, nil
}

// TestDeterministicSensitivityDeclaresItIsHalfBlind is the regression. In
// deterministic mode sensitivity RUNS (its credential layer is model-free) but
// its NER half does not, so a prompt whose only sensitive content is an SSN
// publishes sensitivity:"none" — a confident negative from a check that never
// happened. Because the pass ran, it is (correctly) absent from facets_skipped,
// which before this change left the loss invisible on the wire.
func TestDeterministicSensitivityDeclaresItIsHalfBlind(t *testing.T) {
	p := enrich.Run(ssnPrompt, "claude_code", enrich.Meta{}, nil)

	if p.Sensitivity.Value != "none" {
		t.Fatalf("sensitivity = %+v; this test's premise is a nothing-found result", p.Sensitivity)
	}
	if !contains(p.FacetsDegraded, "sensitivity") {
		t.Fatalf("facets_degraded = %v, want sensitivity: half the pass did not run and %q is a positive claim", p.FacetsDegraded, p.Sensitivity.Value)
	}
	if contains(p.FacetsSkipped, "sensitivity") {
		t.Fatalf("facets_skipped = %v: sensitivity RAN; degraded is not skipped", p.FacetsSkipped)
	}
	// A reduced pass is still not a failed one.
	if p.PipelineStatus != "enriched" {
		t.Fatalf("status = %q, want enriched: nothing failed", p.PipelineStatus)
	}
}

// TestDegradedIsAGeneralCapability: any pass may declare reduced capability,
// the way modelFreeExtractor lets one declare it needs no Model. A pass that
// declares nothing, or declares false, is not named.
func TestDegradedIsAGeneralCapability(t *testing.T) {
	p := enrich.Run(ssnPrompt, "claude_code", enrich.Meta{}, enrichtest.NewFake(),
		enrich.WithCustomExtractors([]enrich.Extractor{degradablePass{degraded: true}}, nil))
	if p.PipelineStatus != "enriched" {
		// "gated" would mean the custom pass never ran and the assertion below
		// proves nothing; a degraded pass must not move the status either.
		t.Fatalf("status = %q, want enriched", p.PipelineStatus)
	}
	if !contains(p.FacetsDegraded, "half_blind") {
		t.Fatalf("facets_degraded = %v, want half_blind", p.FacetsDegraded)
	}

	q := enrich.Run(ssnPrompt, "claude_code", enrich.Meta{}, enrichtest.NewFake(),
		enrich.WithCustomExtractors([]enrich.Extractor{degradablePass{degraded: false}}, nil))
	if len(q.FacetsDegraded) != 0 {
		t.Fatalf("facets_degraded = %v, want none: the pass declared full capability", q.FacetsDegraded)
	}
}

// TestAutoModeReportsNoDegradedFacets: with a real Model every pass runs whole,
// so the default mode's wire shape is unchanged.
func TestAutoModeReportsNoDegradedFacets(t *testing.T) {
	p := enrich.Run(ssnPrompt, "claude_code", enrich.Meta{}, enrichtest.NewFake())
	if p.PipelineStatus != "enriched" {
		t.Fatalf("status = %q, want enriched: a gated run would prove nothing", p.PipelineStatus)
	}
	if len(p.FacetsDegraded) != 0 {
		t.Fatalf("facets_degraded = %v, want none: the model ran", p.FacetsDegraded)
	}
	if p.Sensitivity.Value == "none" {
		t.Fatalf("premise check: the fake model should find the SSN, got %+v", p.Sensitivity)
	}
}

// TestFacetListsAreSubsetsOfExtractorVersions pins the invariant that was only
// ever asserted in prose: every name in facets_skipped / facets_degraded is a
// registered pass, i.e. a key of extractor_versions. A reader resolves those
// names against that map; a name absent from it is unresolvable. The existing
// deterministic-mode test hardcodes seven names, which catches a NEW
// model-dependent pass but says nothing about the relationship.
func TestFacetListsAreSubsetsOfExtractorVersions(t *testing.T) {
	for _, tc := range []struct {
		name string
		p    enrich.Profile
	}{
		{"deterministic", enrich.Run(ssnPrompt, "claude_code", enrich.Meta{}, nil)},
		{"deterministic+custom", enrich.Run(ssnPrompt, "claude_code", enrich.Meta{}, nil,
			enrich.WithCustomExtractors([]enrich.Extractor{degradablePass{degraded: true}}, nil))},
		{"auto", enrich.Run(ssnPrompt, "claude_code", enrich.Meta{}, enrichtest.NewFake())},
	} {
		if len(tc.p.FacetsSkipped) == 0 && len(tc.p.FacetsDegraded) == 0 && tc.name != "auto" {
			t.Fatalf("%s: neither list populated; the case proves nothing", tc.name)
		}
		for field, names := range map[string][]string{
			"facets_skipped":  tc.p.FacetsSkipped,
			"facets_degraded": tc.p.FacetsDegraded,
		} {
			for _, n := range names {
				if _, ok := tc.p.ExtractorVersions[n]; !ok {
					t.Fatalf("%s: %s names %q, absent from extractor_versions %v", tc.name, field, n, keysOf(tc.p.ExtractorVersions))
				}
			}
		}
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
