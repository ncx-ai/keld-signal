package eval

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
)

func TestRunModelOnDeterministicBaseline(t *testing.T) {
	gold, err := LoadGold()
	if err != nil {
		t.Fatal(err)
	}
	pred := RunModel(enrichtest.NewFake(), gold)
	if len(pred) != len(gold) {
		t.Fatalf("pred len = %d, want %d", len(pred), len(gold))
	}
	m := Score(gold, pred, []string{"task_type", "domain", "sensitivity"})

	// Diagnostic baseline over the expanded gold set. The deterministic backend
	// catches the regex-detectable sensitive rows (SSN, API keys, credit cards,
	// email/phone) but NOT the ones with no lexical signal (proprietary roadmaps,
	// address-only PII, MRN-based PHI). So sensitive_recall is > 0 but < 1 here —
	// that gap is exactly what the GLiNER2 sidecar is expected to close (see the
	// build-tagged sidecar eval gate). Assert the value is sane and record it.
	got := m["sensitivity"]["sensitive_recall"]
	if got <= 0.0 || got > 1.0 {
		t.Fatalf("deterministic sensitive_recall = %v, want in (0,1]", got)
	}
	if _, ok := m["task_type"]["accuracy"]; !ok {
		t.Fatal("task_type accuracy missing")
	}
	t.Logf("deterministic baseline over %d gold rows: %+v", len(gold), m)
}
