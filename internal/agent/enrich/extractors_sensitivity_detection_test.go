package enrich

import "testing"

// The sensitivity facet DETECTS; it does not ask anything to classify. These
// tests pin that: the published value is a deterministic rollup of which
// detectors fired, and a model's own opinion about a sensitivity LABEL has no
// path to the output — nor is one ever asked for. Since the facet no longer
// consults GLiNER2 at all (see extractors_sensitivity_modelfree_test.go), the
// model here is present only to be ignored.

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
// backend. The pass must make NONE: its two evidence sources (the gitleaks
// credential layer and the presidio scan behind /pii) are both model-free, so
// re-wiring GLiNER2 into this facet — as a classifier OR as a detector — fails
// this test rather than silently restoring the arm.
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

func TestSensitivityTouchesTheModelNotAtAll(t *testing.T) {
	m := &recordingModel{}
	ctx := NewJobContext("mail me at a@b.io", "claude_code", Meta{}, m)
	if _, err := (SensitivityExtractor{Scan: scanOf([2]string{"email", "a@b.io"})}).Run(ctx); err != nil {
		t.Fatal(err)
	}
	if len(m.classifyTasks) != 0 || len(m.extractTasks) != 0 {
		t.Fatalf("sensitivity asked the model to classify %v / %v; it must ask the model nothing",
			m.classifyTasks, m.extractTasks)
	}
	if m.entityCalls != 0 {
		t.Fatalf("entity calls = %d, want 0: detection is presidio's and gitleaks', not GLiNER2's", m.entityCalls)
	}
}

// The rollup derives the class from the span labels alone, over the two
// detectors that remain. Every row is driven by the PII scan except the
// credential, which is the pure-Go gitleaks layer and needs no backend at all —
// so a row that passes only because some other layer happened to cover it
// would show up as the wrong span label.
func TestSensitivityRollupElevatesFromDetectedEntities(t *testing.T) {
	const ghToken = "ghp_16C7e42F292c6912E7710c838347Ae178B4a"
	for _, tc := range []struct {
		name, label, value, want string
		scanned                  bool
	}{
		{name: "ssn to phi", label: "ssn", value: fxSSN, want: "phi", scanned: true},
		{name: "card to pci", label: "credit_card", value: fxCard, want: "pci", scanned: true},
		{name: "email to pii", label: "email", value: fxEmail, want: "pii", scanned: true},
		{name: "person to pii", label: "person", value: "Marguerite Vandenberg", want: "pii", scanned: true},
		{name: "address to pii", label: "address", value: "14 Kingsway Terrace", want: "pii", scanned: true},
		// The credential layer is not the scan's: it is gitleaks, Go-side, over
		// the full text, and it fires with no scanner wired at all.
		{name: "credential to secrets", label: "api_key", value: ghToken, want: "secrets"},
	} {
		var scan PIIScanner
		if tc.scanned {
			scan = scanOf([2]string{tc.label, tc.value})
		}
		ctx := NewJobContext("context "+tc.value+" trailing", "claude_code", Meta{}, nil)
		out, err := SensitivityExtractor{Scan: scan}.Run(ctx)
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
		spans := out["sensitivity_spans"].([]Entity)
		if len(spans) != 1 || spans[0].Label != tc.label {
			t.Errorf("%s: spans = %+v, want one %s span", tc.name, spans, tc.label)
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
