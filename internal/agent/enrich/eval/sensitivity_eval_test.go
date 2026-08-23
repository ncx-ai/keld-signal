//go:build pii

// Sensitivity gold gate, GLiNER2-FREE. The facet is detection-only — gitleaks
// (internal/agent/enrich/creddetect, pure Go) plus the sidecar's presidio /pii
// route — so it needs no inference model, and gating it behind the full sidecar
// eval hid that: a sensitivity regression could only be seen by someone willing
// to load a 1.9 GB model first.
//
//	SIDECAR_URL=http://127.0.0.1:8477 go test -tags pii ./internal/agent/enrich/eval/ -run Sensitivity -v
package eval

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
)

func TestSensitivityMeetsGoldFloors(t *testing.T) {
	url := os.Getenv("SIDECAR_URL")
	if url == "" {
		url = "http://127.0.0.1:8477"
	}
	sc := sidecar.New(url, 30*time.Second)
	if !sc.Healthy(context.Background()) {
		t.Skipf("sidecar not reachable at %s; run the sidecar and set SIDECAR_URL", url)
	}

	gold, err := LoadGold()
	if err != nil {
		t.Fatal(err)
	}
	// nil Model on purpose: this asserts the facet answers with no GLiNER2 in
	// the process at all. Anything that reintroduces a model dependency into
	// sensitivity fails here rather than passing quietly.
	pred := RunModel(nil, gold, enrich.WithPIIScanner(sc.DetectPII))
	m := Score(gold, pred, []string{"sensitivity"})

	var misses, falsePos int
	for i := range gold {
		g, p := gold[i].Sensitivity, pred[i].Sensitivity
		if g == "" || g == p {
			continue
		}
		if g == "none" {
			falsePos++
			t.Logf("FALSE POSITIVE row %d: pred=%s  %q", i+1, p, gold[i].Text)
		} else {
			misses++
			t.Logf("MISS row %d: want=%s got=%s  %q", i+1, g, p, gold[i].Text)
		}
	}
	t.Logf("gold rows: %d  misses: %d  false positives: %d", len(gold), misses, falsePos)
	t.Logf("sensitivity: %+v", m["sensitivity"])

	if r := m["sensitivity"]["sensitive_recall"]; r < minSensitiveRecall {
		t.Fatalf("sensitive_recall %.3f below floor %.3f", r, minSensitiveRecall)
	}
	if a := m["sensitivity"]["accuracy"]; a < minSensitivityAcc {
		t.Fatalf("sensitivity accuracy %.3f below floor %.3f", a, minSensitivityAcc)
	}
}
