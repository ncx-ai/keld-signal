package daemon

import (
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// fakeFeatureClient advertises every service capability facetsFor probes for,
// PLUS the feature one, so the test below can only pass because facetsFor
// deliberately does not look for it.
type fakeFeatureClient struct{}

func (fakeFeatureClient) Classify(text string, tasks map[string][]string) map[string][]enrich.Ranked {
	return nil
}
func (fakeFeatureClient) Entities(text string, labels map[string]string) []enrich.Entity { return nil }
func (fakeFeatureClient) Extract(text string, labels map[string]string,
	tasks map[string][]string) enrich.ExtractResult {
	return enrich.ExtractResult{}
}
func (fakeFeatureClient) AnalyzeLabeled(path, promptID string, spanMinutes int,
	resolved enrich.ResolvedFacts) (enrich.WindowAnalysis, bool) {
	return enrich.WindowAnalysis{}, false
}
func (fakeFeatureClient) FeatureRowsFor(path, source, sessionID string,
	since *float64, now time.Time, maxRows int,
	resolved enrich.ResolvedFacts) ([]enrich.FeatureVector, *float64, bool) {
	return nil, nil, false
}

// ⚠️ SCOPE: ml_backend "deterministic" ONLY. Under "auto" the signal-embeddings
// subsystem must be ABSENT — never registered, so it appears in neither
// facets_skipped nor extractor_versions, which is this codebase's existing
// distinction between a pass that was SKIPPED and one that was NEVER WIRED
// (WithWorkstreams is the precedent).
//
// The mechanism is that facetsFor — which runs in BOTH modes that have a
// service — does not probe for the capability at all; only deterministicBackend
// attaches it. This test is what stops someone "tidying" the probe into
// facetsFor beside its four siblings, which would silently enable the whole
// path under "auto".
func TestFeatureSourceIsNotWiredByFacetsFor(t *testing.T) {
	svc := facetsFor(fakeFeatureClient{}, nil)
	if svc.Analyze == nil {
		t.Fatal("the fake client is not advertising the service capabilities; the test proves nothing")
	}
	if svc.Features != nil {
		t.Fatal("facetsFor wired the feature source. It must be attached by " +
			"deterministicBackend alone — under ml_backend \"auto\" the " +
			"signal-embeddings path is ABSENT, not skipped.")
	}
}

// featureSourceFor is the probe deterministicBackend uses, and it must find
// exactly what facetsFor declines to.
func TestFeatureSourceForProbesTheCapability(t *testing.T) {
	if featureSourceFor(fakeFeatureClient{}) == nil {
		t.Fatal("featureSourceFor did not recognise a client that has the capability")
	}
	if featureSourceFor(struct{}{}) != nil {
		t.Fatal("featureSourceFor accepted a client that has no such capability")
	}
}

// A nil source (no service this run, or a sidecar too old to answer) must leave
// the emitter unstarted and the advance observer nil, so the watcher's poll
// loop pays a nil check and nothing more.
func TestNoSourceMeansNoEmitterAndNoObserver(t *testing.T) {
	on := func() bool { return true }
	if fn := startFeatureEmitter(t.Context(), nil, "https://x/v1/enrichments",
		func() string { return "tok" }, "actor", "inst", on, on, nil); fn != nil {
		t.Fatal("a nil source still returned an advance observer")
	}
}

// The route derivation is the same one every other /v1/signal/* path uses.
func TestSignalFeaturesEndpoint(t *testing.T) {
	cases := map[string]string{
		"https://atlas.keld.co/v1/enrichments": "https://atlas.keld.co/v1/signal/features",
		"https://atlas.keld.co/v1/foo/bar":     "https://atlas.keld.co/v1/signal/features",
		"https://atlas.keld.co/":               "https://atlas.keld.co/v1/signal/features",
	}
	for in, want := range cases {
		if got := signalFeaturesEndpoint(in); got != want {
			t.Fatalf("signalFeaturesEndpoint(%q) = %q, want %q", in, got, want)
		}
	}
	// ⚠️ It must NOT be the enrichments route. A feature row posted there would
	// be parsed as an EnrichmentIn and stored as one.
	if strings.HasSuffix(signalFeaturesEndpoint("https://atlas.keld.co/v1/enrichments"), "/enrichments") {
		t.Fatal("feature rows would post to the enrichments route")
	}
}
