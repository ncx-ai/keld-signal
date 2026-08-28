package eval

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
)

// TestRunModelOnFakeBaseline sanity-checks the eval harness (RunModel/Score)
// using the offline fake model — NOT a quality gate on any shipped backend.
// The fake (internal/agent/enrich/enrichtest) is a deterministic stand-in used
// only to exercise the harness without a live sidecar; the real quality gate
// for the shipped ML backend is the build-tagged sidecar eval
// (sidecar_eval_test.go, TestSidecarMeetsGoldThresholds).
func TestRunModelOnFakeBaseline(t *testing.T) {
	gold, err := LoadGold()
	if err != nil {
		t.Fatal(err)
	}
	// The PII scan must be wired explicitly: sensitivity takes no evidence from
	// the Model at all, so without it this row would score credentials only and
	// the recall assertion below would be measuring the wrong thing.
	pred := RunModel(enrichtest.NewFake(), gold, enrich.WithPIIScanner(enrichtest.NewScan()))
	if len(pred) != len(gold) {
		t.Fatalf("pred len = %d, want %d", len(pred), len(gold))
	}
	m := Score(gold, pred, []string{"task_type", "domain", "sensitivity"})

	// Diagnostic run over the expanded gold set. Since the 2026-08-23 gold-set
	// correction every sensitive row carries a pattern-detectable value (the
	// rows that asserted the dropped person/address/MRN coverage were re-labelled
	// none), so the fake reaches sensitive_recall 1.0 and the bound below is
	// vacuous on the top end. It still confirms Score/RunModel compute something
	// sane; the real gate is TestSensitivityMeetsGoldFloors (-tags pii), which
	// scores the SHIPPED detectors rather than this stand-in.
	got := m["sensitivity"]["sensitive_recall"]
	if got <= 0.0 || got > 1.0 {
		t.Fatalf("fake sensitive_recall = %v, want in (0,1]", got)
	}
	if _, ok := m["task_type"]["accuracy"]; !ok {
		t.Fatal("task_type accuracy missing")
	}
	t.Logf("fake baseline over %d gold rows: %+v", len(gold), m)
}
