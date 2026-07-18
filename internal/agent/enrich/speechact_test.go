package enrich

import "testing"

// wantLabelModel returns the chosen readable label as top for every task, so we
// can prove the winning text maps back to its canonical id.
type wantLabelModel struct{ want string }

func (m wantLabelModel) Classify(text string, tasks map[string][]string) map[string][]Ranked {
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
func (wantLabelModel) Entities(string, map[string]string) []Entity { return nil }
func (wantLabelModel) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{}
}

func TestSpeechActMapsWinningTextToID(t *testing.T) {
	// pick the description text belonging to id "question"; expect value "question".
	var qText string
	for _, d := range SpeechActDefs {
		if d.ID == "question" {
			qText = d.Text
		}
	}
	if qText == "" {
		t.Fatal("SpeechActDefs missing a 'question' entry")
	}
	out, err := (SpeechActExtractor{}).Run(NewJobContext("how do I reverse a list?", "claude_code", Meta{Tool: "claude_code"}, wantLabelModel{want: qText}))
	if err != nil {
		t.Fatal(err)
	}
	if got := out["speech_act"].(Labeled).Value; got != "question" {
		t.Fatalf("speech_act value = %q, want question", got)
	}
}

func TestSpeechActClassifiesTextNotPreamble(t *testing.T) {
	// classifyLabeled must be handed ctx.Text only — assert the model never sees
	// the preamble's context block for this facet.
	c := &captureText{}
	meta := Meta{Repo: "acme/api", Tool: "claude_code", RecentPrompts: []string{"add validation"}}
	if _, err := (SpeechActExtractor{}).Run(NewJobContext("ship it", "claude_code", meta, c)); err != nil {
		t.Fatal(err)
	}
	if c.seen != "ship it" {
		t.Fatalf("speech_act must classify ctx.Text only; model saw %q", c.seen)
	}
}

// captureText records the text handed to Classify.
type captureText struct{ seen string }

func (c *captureText) Classify(text string, tasks map[string][]string) map[string][]Ranked {
	c.seen = text
	out := map[string][]Ranked{}
	for n, ls := range tasks {
		if len(ls) > 0 {
			out[n] = []Ranked{{Label: ls[0], Confidence: 1}}
		}
	}
	return out
}
func (c *captureText) Entities(string, map[string]string) []Entity { return nil }
func (c *captureText) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{}
}

func TestSpeechActDefsCoverIDs(t *testing.T) {
	want := map[string]bool{"command": true, "question": true, "statement": true, "fragment": true}
	if len(SpeechActDefs) != len(want) {
		t.Fatalf("SpeechActDefs has %d entries, want %d", len(SpeechActDefs), len(want))
	}
	for _, d := range SpeechActDefs {
		if !want[d.ID] {
			t.Fatalf("unexpected speech_act id %q", d.ID)
		}
		delete(want, d.ID)
	}
	for id := range want {
		t.Fatalf("SpeechActDefs missing id %q", id)
	}
}
