package eval

import (
	"testing"

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
	pred := RunModel(enrichtest.NewFake(), gold)
	if len(pred) != len(gold) {
		t.Fatalf("pred len = %d, want %d", len(pred), len(gold))
	}
	m := Score(gold, pred, []string{"task_type", "domain", "sensitivity"})

	// Diagnostic run over the expanded gold set using the fake. The fake
	// catches the regex-detectable sensitive rows (SSN, API keys, credit cards,
	// email/phone) but NOT the ones with no lexical signal (proprietary roadmaps,
	// address-only PII, MRN-based PHI). So sensitive_recall is > 0 but < 1 here —
	// this just confirms Score/RunModel compute something sane, not that the
	// fake meets any bar.
	got := m["sensitivity"]["sensitive_recall"]
	if got <= 0.0 || got > 1.0 {
		t.Fatalf("fake sensitive_recall = %v, want in (0,1]", got)
	}
	if _, ok := m["task_type"]["accuracy"]; !ok {
		t.Fatal("task_type accuracy missing")
	}
	t.Logf("fake baseline over %d gold rows: %+v", len(gold), m)
}
