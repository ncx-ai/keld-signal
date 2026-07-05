//go:build sidecar

// Live eval gate: compares the GLiNER2 sidecar backend against the deterministic
// backend over the gold set. Build-tagged so normal CI (no sidecar) skips it.
//
//	SIDECAR_URL=http://127.0.0.1:8399 go test -tags sidecar ./internal/agent/enrich/eval/ -run Sidecar -v
package eval

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
)

func TestSidecarVsDeterministic(t *testing.T) {
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
	det := Score(gold, RunModel(enrich.NewDeterministic(), gold), fields)
	side := Score(gold, RunModel(sc, gold), fields)

	t.Logf("gold rows: %d", len(gold))
	t.Logf("deterministic: %+v", det)
	t.Logf("sidecar:       %+v", side)

	// The sidecar's value (measured) is the compliance/security dimension:
	// sensitivity accuracy jumps from ~0.23 (regex) to ~0.81, and it catches
	// sensitive spans the regex baseline cannot (proprietary, address-only PII,
	// MRN-based PHI). It is at parity with the keyword baseline on task_type
	// (keyword priors are strong for "write/summarize/translate"), so the gate
	// hard-asserts the sensitivity wins and only guards classification against a
	// real regression (small tolerance absorbs run-to-run noise).

	// Safety-critical hard gate: sensitivity recall must not regress.
	dSR, sSR := det["sensitivity"]["sensitive_recall"], side["sensitivity"]["sensitive_recall"]
	if sSR < dSR {
		t.Fatalf("sidecar sensitive_recall %.3f regressed vs deterministic %.3f", sSR, dSR)
	}
	// Value hard gate: sensitivity CLASSIFICATION must be clearly better — this is
	// the reason the ML backend exists.
	dSA, sSA := det["sensitivity"]["accuracy"], side["sensitivity"]["accuracy"]
	if sSA <= dSA {
		t.Fatalf("sidecar sensitivity accuracy %.3f did not beat deterministic %.3f", sSA, dSA)
	}
	// Classification: allow small noise, fail only on a real regression.
	const tol = 0.05
	for _, f := range []string{"task_type", "domain"} {
		if side[f]["accuracy"] < det[f]["accuracy"]-tol {
			t.Fatalf("sidecar %s accuracy %.3f regressed materially vs deterministic %.3f",
				f, side[f]["accuracy"], det[f]["accuracy"])
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
