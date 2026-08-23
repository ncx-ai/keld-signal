package enrich

import (
	"strings"
	"testing"
)

// NER is used for DETECTION only, never for CLASSIFICATION. These tests pin
// that for the sensitivity facet: the published value is a deterministic
// rollup of which detectors fired, and the model's own opinion about a
// sensitivity LABEL must have no path to the output — nor even be asked for.

// classifyingModel is the backend this change exists to neutralise: it finds no
// entities but answers the sensitivity classification task confidently and
// wrongly. Before the change its "pci"@0.97 reached the published profile;
// after it, nothing it says can.
type classifyingModel struct{ emptyModel }

func (classifyingModel) Classify(_ string, tasks map[string][]string) map[string][]Ranked {
	out := map[string][]Ranked{}
	for task := range tasks {
		out[task] = []Ranked{{Label: "pci", Confidence: 0.97}}
	}
	return out
}

func (m classifyingModel) Extract(text string, labels map[string]string, tasks map[string][]string) ExtractResult {
	return ExtractResult{Results: m.Classify(text, tasks)}
}

func TestSensitivityClassificationCannotReachOutput(t *testing.T) {
	ctx := NewJobContext("just refactor the retry loop please", "claude_code", Meta{}, classifyingModel{})
	out, err := SensitivityExtractor{}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	lab := out["sensitivity"].(Labeled)
	if lab.Value != "none" {
		t.Fatalf("sensitivity = %q, want none: no detector fired, so the model's label must not reach the output", lab.Value)
	}
}

// recordingModel captures every request the sensitivity pass makes of the
// backend, so re-adding a classification task fails this test rather than
// silently restoring the arm.
type recordingModel struct {
	emptyModel
	classifyTasks []string
	extractTasks  []string
	entityCalls   int
}

func (m *recordingModel) Classify(_ string, tasks map[string][]string) map[string][]Ranked {
	for task := range tasks {
		m.classifyTasks = append(m.classifyTasks, task)
	}
	return nil
}

func (m *recordingModel) Entities(string, map[string]string) []Entity {
	m.entityCalls++
	return nil
}

func (m *recordingModel) Extract(_ string, _ map[string]string, tasks map[string][]string) ExtractResult {
	for task := range tasks {
		m.extractTasks = append(m.extractTasks, task)
	}
	return ExtractResult{}
}

func TestSensitivityRequestsNoClassificationTask(t *testing.T) {
	m := &recordingModel{}
	ctx := NewJobContext("mail me at a@b.io", "claude_code", Meta{}, m)
	if _, err := (SensitivityExtractor{}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(m.classifyTasks) != 0 || len(m.extractTasks) != 0 {
		t.Fatalf("sensitivity asked the model to classify %v / %v; it must perform entity DETECTION only",
			m.classifyTasks, m.extractTasks)
	}
	if m.entityCalls != 1 {
		t.Fatalf("entity calls = %d, want exactly 1 (the pure /entities detection path)", m.entityCalls)
	}
}

// nerOnlyModel is a detector: it reports spans and holds no opinion about the
// class. The rollup must derive the class from the span labels alone.
type nerOnlyModel struct {
	emptyModel
	label, needle string
}

func (m nerOnlyModel) Entities(text string, _ map[string]string) []Entity {
	i := strings.Index(text, m.needle)
	if i < 0 {
		return nil
	}
	return []Entity{{Label: m.label, Text: m.needle, Start: i, End: i + len(m.needle), Confidence: 0.9}}
}

func TestSensitivityRollupElevatesFromDetectedEntities(t *testing.T) {
	for _, tc := range []struct {
		name, label, value, want string
	}{
		{"ssn to phi", "ssn", fxSSN, "phi"},
		{"card to pci", "credit_card", fxCard, "pci"},
		{"credential to secrets", "api_key", "ghp_16C7e42F292c6912E7710c838347Ae178B4a", "secrets"},
		{"email to pii", "email", "dana.reeve@northwind-labs.co", "pii"},
		// person has no pattern for the scan to corroborate, so this case can
		// only pass if the NER DETECTION path is actually wired to the backend.
		{"person to pii", "person", "Marguerite Vandenberg", "pii"},
	} {
		m := nerOnlyModel{label: tc.label, needle: tc.value}
		ctx := NewJobContext("context "+tc.value+" trailing", "claude_code", Meta{}, m)
		// The pattern types are admitted only where the gated scan corroborates
		// them; person/api_key carry no pattern and need none.
		out, err := SensitivityExtractor{Scan: scanOf([2]string{tc.label, tc.value})}.Run(ctx)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		lab := out["sensitivity"].(Labeled)
		if lab.Value != tc.want {
			t.Errorf("%s: sensitivity = %q, want %q", tc.name, lab.Value, tc.want)
		}
		if lab.Confidence != 1.0 {
			t.Errorf("%s: confidence = %v, want 1.0", tc.name, lab.Confidence)
		}
		if len(out["sensitivity_spans"].([]Entity)) == 0 {
			t.Errorf("%s: expected the detected span to be published", tc.name)
		}
	}
}

// "none" is a report about the detector set, not a hunch. It carries the same
// confidence as a positive rollup, because it is the same kind of statement:
// the detectors ran, and this is what they found. A residual 0.0 would read as
// "we are not sure it is none" — which was only ever true of the classifier
// that used to produce it.
func TestSensitivityNoneIsAConfidentDetectorReport(t *testing.T) {
	for _, tc := range []struct {
		name  string
		model Model
	}{
		{"with a model", emptyModel{}},
		{"deterministic mode (nil model)", nil},
	} {
		ctx := NewJobContext("bump the timeout to 30s", "claude_code", Meta{}, tc.model)
		out, err := SensitivityExtractor{}.Run(ctx)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		lab := out["sensitivity"].(Labeled)
		if lab.Value != "none" {
			t.Errorf("%s: sensitivity = %q, want none", tc.name, lab.Value)
		}
		if lab.Confidence != 1.0 {
			t.Errorf("%s: confidence = %v, want 1.0 (no detector fired is a finding, not an uncertainty)", tc.name, lab.Confidence)
		}
	}
}

// Deterministic mode is unchanged: with no model at all the credential layer
// still fires and still rolls up.
func TestSensitivityDeterministicModeUnchanged(t *testing.T) {
	ctx := NewJobContext("token ghp_16C7e42F292c6912E7710c838347Ae178B4a", "claude_code", Meta{}, nil)
	out, err := SensitivityExtractor{}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := out["sensitivity"].(Labeled).Value; got != "secrets" {
		t.Fatalf("sensitivity = %q, want secrets", got)
	}
	if len(out["sensitivity_spans"].([]Entity)) == 0 {
		t.Fatal("expected a masked credential span")
	}
}
