package enrich_test

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
)

// nerOnlyPrompt carries concrete PII that ONLY the NER can find: a person name.
// piidetect covers ssn/credit_card/email deterministically, so a prompt built
// on any of those would no longer demonstrate a half-blind pass — person and
// address are what the missing half still owns. It is content-ful so the gate
// never fires.
const nerOnlyPrompt = `Please update the customer record for Dana Rivers, ` +
	`the account holder, in the billing export.`

// ssnPrompt carries a synthetic SSN — structurally valid, on no published
// example list (the textbook 123-45-6789 is excluded by piidetect's well-known
// gate, deliberately, so documentation constants never report phi).
const ssnPrompt = `Please update the customer record, ` +
	`social security number 321-54-9876, in the billing export.`

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
// deterministic mode sensitivity RUNS (its model-free layers cover credentials
// and the regular PII formats) but its NER half does not, so a prompt whose
// only sensitive content is a person name publishes sensitivity:"none" — a
// confident negative from a check that never happened. Because the pass ran, it
// is (correctly) absent from facets_skipped, which before this change left the
// loss invisible on the wire.
func TestDeterministicSensitivityDeclaresItIsHalfBlind(t *testing.T) {
	p := enrich.Run(nerOnlyPrompt, "claude_code", enrich.Meta{}, nil)

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
	p := enrich.Run(nerOnlyPrompt, "claude_code", enrich.Meta{}, enrichtest.NewFake(),
		enrich.WithCustomExtractors([]enrich.Extractor{degradablePass{degraded: true}}, nil))
	if p.PipelineStatus != "enriched" {
		// "gated" would mean the custom pass never ran and the assertion below
		// proves nothing; a degraded pass must not move the status either.
		t.Fatalf("status = %q, want enriched", p.PipelineStatus)
	}
	if !contains(p.FacetsDegraded, "half_blind") {
		t.Fatalf("facets_degraded = %v, want half_blind", p.FacetsDegraded)
	}

	q := enrich.Run(nerOnlyPrompt, "claude_code", enrich.Meta{}, enrichtest.NewFake(),
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
		{"deterministic", enrich.Run(nerOnlyPrompt, "claude_code", enrich.Meta{}, nil)},
		{"deterministic+custom", enrich.Run(nerOnlyPrompt, "claude_code", enrich.Meta{}, nil,
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

// TestDeterministicPHIIsNotQualified is the other half of the marker's honesty.
// Before piidetect, "no model" meant the pass could not reach phi or pci at all,
// so every deterministic-mode answer was qualified. Now the SSN is found without
// a model, and the answer is at the CEILING of the severity order: the entity
// types the missing NER half still owns (person, address, phone) all roll up to
// "pii", so nothing it could have added would change this result. Naming the
// facet degraded here would be crying wolf, and a marker that fires on a correct
// answer stops being read on the ones where it matters.
func TestDeterministicPHIIsNotQualified(t *testing.T) {
	p := enrich.Run(ssnPrompt, "claude_code", enrich.Meta{}, nil)

	if p.Sensitivity.Value != "phi" {
		t.Fatalf("sensitivity = %+v, want phi: the SSN must be found with no model present", p.Sensitivity)
	}
	if contains(p.FacetsDegraded, "sensitivity") {
		t.Fatalf("facets_degraded = %v: phi is the top class, nothing the NER adds could raise it", p.FacetsDegraded)
	}
	if len(p.SensitivitySpans) != 1 || p.SensitivitySpans[0].Label != "ssn" {
		t.Fatalf("sensitivity_spans = %+v, want one ssn span", p.SensitivitySpans)
	}
}
