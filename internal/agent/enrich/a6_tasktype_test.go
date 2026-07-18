package enrich

import "testing"

// pickLabelModel makes the classifier prefer a chosen readable label text
// (returning it top) so we can prove which label set task_type classified
// against and that the id is mapped back correctly.
type pickLabelModel struct{ want string }

func (m pickLabelModel) Classify(text string, tasks map[string][]string) map[string][]Ranked {
	out := map[string][]Ranked{}
	for name, labels := range tasks {
		if len(labels) == 0 {
			continue
		}
		top := labels[0]
		for _, l := range labels {
			if l == m.want {
				top = l
			}
		}
		out[name] = []Ranked{{Label: top, Confidence: 1}}
	}
	return out
}
func (pickLabelModel) Entities(string, map[string]string) []Entity { return nil }
func (pickLabelModel) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{}
}

func taskType(t *testing.T, m Model) Labeled {
	t.Helper()
	out, err := (TaskTypeExtractor{}).Run(NewJobContext("fix the timezone bug", "claude_code", Meta{Tool: "claude_code"}, m))
	if err != nil {
		t.Fatal(err)
	}
	lbl, _ := out["task_type"].(Labeled)
	return lbl
}

func TestTaskTypeDescriptions(t *testing.T) {
	// Default (no env): A6 on — task_type classifies over the readable
	// descriptions and maps the winning text back to its canonical id.
	got := taskType(t, pickLabelModel{want: "software engineering"})
	if got.Value != "codegen" {
		t.Fatalf("A6 default-on: winning label %q should map to id codegen; got %s", "software engineering", got.Value)
	}

	// The description path must be what's used: a model that only knows the bare
	// id string "codegen" finds no such label (defs use descriptions), so it
	// falls through to the first candidate's id — still a valid canonical id,
	// never a raw description leaking out as the value.
	for _, d := range TaskTypeDefs {
		if d.Text == d.ID && d.ID != "other" {
			t.Fatalf("A6 def %q uses a bare id as its text; expected a readable description", d.ID)
		}
	}

	// Escape hatch: disable restores bare-string classification (labels ARE the
	// ids), so a model picking the bare id "summarization" yields that id.
	t.Setenv("KELD_ENRICH_TASKTYPE_DESCRIPTIONS", "off")
	if got := taskType(t, pickLabelModel{want: "summarization"}); got.Value != "summarization" {
		t.Fatalf("disable escape-hatch must classify over bare ids; got %s", got.Value)
	}
}

// TestTaskTypeDefsCoverVocab guards that the A6 description set stays in lockstep
// with the canonical TaskTypes vocabulary — every id present, no extras, so a
// vocab change can't silently leave a task_type undescribed.
func TestTaskTypeDefsCoverVocab(t *testing.T) {
	if len(TaskTypeDefs) != len(TaskTypes) {
		t.Fatalf("TaskTypeDefs has %d entries; TaskTypes has %d", len(TaskTypeDefs), len(TaskTypes))
	}
	want := map[string]bool{}
	for _, tt := range TaskTypes {
		want[tt] = true
	}
	for _, d := range TaskTypeDefs {
		if !want[d.ID] {
			t.Fatalf("TaskTypeDefs id %q is not in TaskTypes", d.ID)
		}
		delete(want, d.ID)
	}
	for id := range want {
		t.Fatalf("TaskTypes id %q has no TaskTypeDefs description", id)
	}
}
