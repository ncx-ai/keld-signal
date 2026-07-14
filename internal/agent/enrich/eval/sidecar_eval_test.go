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

// Absolute gold-set thresholds for the sidecar backend. These are conservative
// FLOORS, not targets — the deterministic backend has been purged, so there is
// no baseline to compare against, and these were picked without a live sidecar
// run against the current (expanded) gold set. Tighten them once a real run
// establishes the sidecar's actual scores; don't loosen them without one.
const (
	minSensitiveRecall = 0.60
	minSensitivityAcc  = 0.60
	minTaskTypeAcc     = 0.60
	minDomainAcc       = 0.60
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
