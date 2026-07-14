//go:build sidecar

// Live eval gate: scores the GLiNER2 sidecar backend against fixed absolute
// thresholds on the gold set. Build-tagged so normal CI (no sidecar) skips it.
//
//	SIDECAR_URL=http://127.0.0.1:8399 go test -tags sidecar ./internal/agent/enrich/eval/ -run Sidecar -v
package eval

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
)

// Absolute gold-set thresholds for the sidecar backend — regression FLOORS, not
// targets. Calibrated against a live gliner2-large-v1 run on 2026-07-14 (73-row
// gold set, no-context RunModel), which measured: sensitive_recall 0.565,
// sensitivity_acc 0.811, task_type_acc 0.580, domain_acc 0.449. Floors sit ~0.05
// below those so genuine regressions fail the gate while run-to-run noise doesn't.
// Raise them if the sidecar/model improves; don't lower them without a fresh run.
// (These no-context scores understate production, which feeds Meta.Preamble; the
// facet augmentation check below exercises the context path.)
const (
	minSensitiveRecall = 0.50
	minSensitivityAcc  = 0.75
	minTaskTypeAcc     = 0.50
	minDomainAcc       = 0.40
)

func TestSidecarMeetsGoldThresholds(t *testing.T) {
	url := os.Getenv("SIDECAR_URL")
	if url == "" {
		url = "http://127.0.0.1:8399"
	}
	sc := sidecar.New(url, 30*time.Second)
	if !sc.Healthy(context.Background()) {
		t.Skipf("sidecar not reachable at %s; run the sidecar and set SIDECAR_URL", url)
	}

	gold, err := LoadGold()
	if err != nil {
		t.Fatal(err)
	}
	fields := []string{"task_type", "domain", "sensitivity"}
	side := Score(gold, RunModel(sc, gold), fields)

	t.Logf("gold rows: %d", len(gold))
	t.Logf("sidecar: %+v", side)

	// Safety-critical hard gate: sensitivity recall must clear the floor — this
	// is the compliance/security dimension the ML backend exists to deliver.
	if sSR := side["sensitivity"]["sensitive_recall"]; sSR < minSensitiveRecall {
		t.Fatalf("sidecar sensitive_recall %.3f below floor %.3f", sSR, minSensitiveRecall)
	}
	// Value hard gate: sensitivity CLASSIFICATION accuracy must clear the floor.
	if sSA := side["sensitivity"]["accuracy"]; sSA < minSensitivityAcc {
		t.Fatalf("sidecar sensitivity accuracy %.3f below floor %.3f", sSA, minSensitivityAcc)
	}
	// Classification floors for the remaining fields.
	floors := map[string]float64{"task_type": minTaskTypeAcc, "domain": minDomainAcc}
	for _, f := range []string{"task_type", "domain"} {
		if got := side[f]["accuracy"]; got < floors[f] {
			t.Fatalf("sidecar %s accuracy %.3f below floor %.3f", f, got, floors[f])
		}
	}

	// Augmentation lift: feed each gold row's session context (recent prompts,
	// branch, project) into the sidecar classifier and compare against the
	// no-context baseline on the facets that actually flow through the
	// preamble (function_guess, subcategory). This is the measurement the
	// eval previously never took — GoldRow.Meta was built but never wired
	// into a model run.
	facets := []string{"function_guess", "subcategory"}
	fBase := Score(gold, RunModel(sc, gold), facets)
	fAug := Score(gold, RunModelWithContext(sc, gold), facets)
	t.Logf("facets baseline:  %+v", fBase)
	t.Logf("facets augmented: %+v", fAug)
	for _, f := range facets {
		// augmentation must not regress facet accuracy (small tolerance for run-to-run noise)
		if fAug[f]["accuracy"]+0.01 < fBase[f]["accuracy"] {
			t.Errorf("augmentation regressed %s: base=%.3f aug=%.3f", f, fBase[f]["accuracy"], fAug[f]["accuracy"])
		}
	}
}
