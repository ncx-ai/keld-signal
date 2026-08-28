package llmstudy

import (
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// enumOf pulls the enum list a schema declares for a property.
func enumOf(t *testing.T, schema map[string]any, prop string) []string {
	t.Helper()
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %v", schema)
	}
	p, ok := props[prop].(map[string]any)
	if !ok {
		t.Fatalf("schema has no property %q", prop)
	}
	raw, ok := p["enum"].([]string)
	if !ok {
		t.Fatalf("property %q has no []string enum: %v", prop, p)
	}
	return raw
}

// The study is only a fair comparison if it scores the SAME taxonomy the
// pipeline ships. Reading the vocabulary from enrich means a vocab change breaks
// this test rather than silently skewing the study.
func TestWaveOneEnumsMatchLiveVocabulary(t *testing.T) {
	s := WaveOneSchema()
	cases := []struct {
		prop string
		defs []enrich.LabelDef
	}{
		{"task_type", enrich.TaskTypeDefs},
		{"domain", enrich.DomainDefs},
		{"activity_type", enrich.Activities},
		{"personal", enrich.Personal},
		{"function_guess", enrich.Functions},
	}
	for _, c := range cases {
		got := enumOf(t, s, c.prop)
		if len(got) != len(c.defs) {
			t.Fatalf("%s: enum has %d entries, vocabulary has %d", c.prop, len(got), len(c.defs))
		}
		for i, d := range c.defs {
			if got[i] != d.ID {
				t.Errorf("%s[%d] = %q, want %q", c.prop, i, got[i], d.ID)
			}
		}
	}
}

func TestWaveOneSchemaIsStrict(t *testing.T) {
	s := WaveOneSchema()
	if s["additionalProperties"] != false {
		t.Error("schema must set additionalProperties:false so the model cannot invent fields")
	}
	req, ok := s["required"].([]string)
	if !ok || len(req) != 5 {
		t.Fatalf("required = %v, want all 5 wave-1 facets", s["required"])
	}
}

func TestSubcategorySchemaIsConditionedOnFunction(t *testing.T) {
	got := enumOf(t, SubcategorySchema("eng"), "subcategory")
	want := enrich.Subcats["eng"]
	if len(got) != len(want) {
		t.Fatalf("eng subcats: got %d, want %d", len(got), len(want))
	}
	for i, d := range want {
		if got[i] != d.ID {
			t.Errorf("subcategory[%d] = %q, want %q", i, got[i], d.ID)
		}
	}
	if SubcategorySchema("nonexistent") != nil {
		t.Error("unknown function must yield a nil schema, not an empty enum")
	}
}

// The readable descriptions are load-bearing in the encoder pipeline; the LLM is
// given the same wording so the two are genuinely solving the same task.
func TestWaveOnePromptCarriesDescriptionsAndWindow(t *testing.T) {
	w := mineFixture(t, 8)[1]
	p := WaveOnePrompt(w)
	for _, d := range enrich.DomainDefs {
		if !strings.Contains(p, d.Text) {
			t.Errorf("prompt omits domain description %q", d.Text)
		}
	}
	if !strings.Contains(p, Render(w)) {
		t.Error("prompt omits the rendered window")
	}
	if !strings.Contains(p, w.Target) {
		t.Error("prompt must name the target prompt explicitly")
	}
}

func TestSubcategoryPromptNamesTheFunction(t *testing.T) {
	w := mineFixture(t, 8)[1]
	p := SubcategoryPrompt(w, "eng")
	if !strings.Contains(p, `"eng"`) {
		t.Error("subcategory prompt must state the already-determined function")
	}
	for _, d := range enrich.Subcats["eng"] {
		if !strings.Contains(p, d.Text) {
			t.Errorf("subcategory prompt omits description %q", d.Text)
		}
	}
	// A different function's subcategories must not appear.
	for _, d := range enrich.Subcats["mkt"] {
		if strings.Contains(p, d.ID) {
			t.Errorf("subcategory prompt leaked an out-of-function option %q", d.ID)
		}
	}
}
