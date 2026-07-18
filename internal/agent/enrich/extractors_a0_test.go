package enrich

import (
	"strings"
	"testing"
)

// captureClassify records the text passed to Classify and returns the first
// candidate label per task, so we can assert what the task_type pass feeds the model.
type captureClassify struct{ last string }

func (c *captureClassify) Classify(text string, tasks map[string][]string) map[string][]Ranked {
	c.last = text
	out := map[string][]Ranked{}
	for n, ls := range tasks {
		if len(ls) > 0 {
			out[n] = []Ranked{{Label: ls[0], Confidence: 1}}
		}
	}
	return out
}
func (c *captureClassify) Entities(string, map[string]string) []Entity { return nil }
func (c *captureClassify) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{}
}

// TestTaskTypeAlwaysUsesContext proves A0 is unconditional: task_type always
// classifies over Meta.Preamble()+text, the same context the other classifiers
// already use via classifyPass. No flag, no raw-text path.
func TestTaskTypeAlwaysUsesContext(t *testing.T) {
	meta := Meta{Repo: "acme/api", Tool: "claude_code", RecentPrompts: []string{"add request validation"}}

	c := &captureClassify{}
	if _, err := (TaskTypeExtractor{}).Run(NewJobContext("do the thing", "claude_code", meta, c)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(c.last, "add request validation") || !strings.Contains(c.last, "Task: do the thing") {
		t.Fatalf("task_type must always be given the context preamble; got %q", c.last)
	}
}
