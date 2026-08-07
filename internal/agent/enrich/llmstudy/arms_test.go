package llmstudy

import (
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// fakeModel records every text it was asked to classify and returns the first
// label of each task, giving a deterministic in-vocabulary answer.
type fakeModel struct{ seen []string }

func (m *fakeModel) Classify(text string, tasks map[string][]string) map[string][]enrich.Ranked {
	m.seen = append(m.seen, text)
	out := map[string][]enrich.Ranked{}
	for name, labels := range tasks {
		if len(labels) > 0 {
			out[name] = []enrich.Ranked{{Label: labels[0], Confidence: 0.9}}
		}
	}
	return out
}

func (m *fakeModel) Entities(text string, labels map[string]string) []enrich.Entity { return nil }

func (m *fakeModel) Extract(text string, labels map[string]string, tasks map[string][]string) enrich.ExtractResult {
	m.seen = append(m.seen, text)
	return enrich.ExtractResult{}
}

// The control must never see the rendered window: gliner2 head-truncates and the
// window puts the target LAST, so the prompt under classification would be the
// first thing discarded.
func TestEncoderArmReceivesProductionInputNotTheWindow(t *testing.T) {
	w := mineFixture(t, 8)[1]
	fm := &fakeModel{}
	got := NewEncoderArm(fm).Classify(w)

	if !got.Valid {
		t.Fatalf("Valid=false, Err=%q", got.Err)
	}
	if len(fm.seen) == 0 {
		t.Fatal("model was never called")
	}
	rendered := Render(w)
	for _, s := range fm.seen {
		if s == rendered {
			t.Fatal("encoder arm was fed the rendered window instead of production input")
		}
		if strings.Contains(s, "\ntool: ") || strings.HasPrefix(s, "tool: ") {
			t.Errorf("encoder arm input contains rendered tool lines:\n%s", s)
		}
		if strings.Contains(s, "assistant: ") {
			t.Errorf("encoder arm input contains rendered assistant turns:\n%s", s)
		}
	}
}

// The target prompt must actually reach the model, or the control is measuring
// nothing.
func TestEncoderArmClassifiesTheTargetPrompt(t *testing.T) {
	w := mineFixture(t, 8)[1]
	fm := &fakeModel{}
	NewEncoderArm(fm).Classify(w)

	var found bool
	for _, s := range fm.seen {
		if strings.Contains(s, w.Target) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("target %q never reached the model; saw %d inputs", w.Target, len(fm.seen))
	}
}

func TestEncoderArmPopulatesScoredFacets(t *testing.T) {
	got := NewEncoderArm(&fakeModel{}).Classify(mineFixture(t, 8)[1])
	for _, f := range []Facet{FacetTaskType, FacetDomain, FacetActivity, FacetPersonal, FacetFunction} {
		if got.Labels[f] == "" {
			t.Errorf("facet %s not populated", f)
		}
	}
}

func TestEncoderArmRecordsLatency(t *testing.T) {
	if got := NewEncoderArm(&fakeModel{}).Classify(mineFixture(t, 8)[1]); got.LatencyMS < 0 {
		t.Error("LatencyMS not recorded")
	}
}

// WithMaxLen must pass a non-sidecar Model through untouched — the daemon's
// bindMaxLen has the same contract, so a test fake keeps working.
func TestWithMaxLenPassesThroughNonSidecarModel(t *testing.T) {
	fm := &fakeModel{}
	arm := NewEncoderArm(fm)
	if got := arm.WithMaxLen(768); got.Model != enrich.Model(fm) {
		t.Fatal("a non-sidecar Model must pass through unchanged")
	}
	if got := arm.WithMaxLen(0); got != arm {
		t.Fatal("n <= 0 must return the arm unchanged")
	}
}

// A capped arm must still classify correctly — the cap binds the backend, it does
// not change what the arm does.
func TestCappedArmStillClassifies(t *testing.T) {
	got := NewEncoderArm(&fakeModel{}).WithMaxLen(768).Classify(mineFixture(t, 8)[1])
	if !got.Valid {
		t.Fatalf("Valid=false, Err=%q", got.Err)
	}
	if got.Labels[FacetDomain] == "" {
		t.Error("capped arm produced no domain label")
	}
}
