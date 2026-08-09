package llmstudy

import "testing"

// The gold set must validate itself. A typo in gold would otherwise be reported as a
// model failure, which is the worst possible way for an eval to be wrong.
func TestGalleryGoldIsValid(t *testing.T) {
	rows, err := LoadGalleryGold("testdata/gallery_gold.jsonl")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(rows) < 40 {
		t.Fatalf("only %d gold rows; too few to measure per-template", len(rows))
	}
	for _, p := range ValidateGalleryGold(rows) {
		t.Errorf("gold: %s", p)
	}
	t.Logf("%d gold rows validated", len(rows))
}

// Precision is half the measurement, so every template needs rows where the correct
// answer is "nothing". An eval with only positives cannot detect over-firing.
func TestGalleryGoldHasNegativesPerTemplate(t *testing.T) {
	rows, err := LoadGalleryGold("testdata/gallery_gold.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	total, neg := map[string]int{}, map[string]int{}
	for _, g := range rows {
		total[g.Template]++
		empty := true
		for _, spans := range g.Entities {
			if len(spans) > 0 {
				empty = false
			}
		}
		for _, v := range g.Fields {
			if v != "" {
				empty = false
			}
		}
		if g.Label != "" || len(g.Labels) > 0 {
			empty = false
		}
		if empty {
			neg[g.Template]++
		}
	}
	for tmpl, n := range total {
		tp, _ := GalleryByID(tmpl)
		// Label templates always assign a value, so "nothing" is not expressible.
		if tp.Kind == KindSingleLabel {
			continue
		}
		if neg[tmpl] == 0 {
			t.Errorf("template %s has %d rows but no negative — cannot measure precision", tmpl, n)
		}
	}
	t.Logf("negatives per template: %v", neg)
}

func TestGallerySchemasAreWellFormed(t *testing.T) {
	for _, tp := range GalleryTemplates {
		s := GallerySchema(tp)
		if s == nil {
			t.Errorf("%s: nil schema for kind %s", tp.ID, tp.Kind)
			continue
		}
		if s["additionalProperties"] != false {
			t.Errorf("%s: schema must be strict", tp.ID)
		}
		props, ok := s["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Errorf("%s: schema has no properties", tp.ID)
		}
		switch tp.Kind {
		case KindEntity:
			for _, ty := range tp.Types {
				if _, ok := props[ty.Name]; !ok {
					t.Errorf("%s: schema missing entity type %q", tp.ID, ty.Name)
				}
			}
		case KindStructure:
			for _, f := range tp.Fields {
				if _, ok := props[f.Name]; !ok {
					t.Errorf("%s: schema missing field %q", tp.ID, f.Name)
				}
			}
		}
	}
}

// The prompt must tell the model that "nothing" is a valid answer, or precision on
// the negative rows measures prompt design rather than capability.
func TestGalleryPromptPermitsEmptyAnswers(t *testing.T) {
	for _, tp := range GalleryTemplates {
		p := GalleryPrompt(tp, "some prompt text")
		if !contains(p, tp.Desc) {
			t.Errorf("%s: prompt omits the model-facing description", tp.ID)
		}
		switch tp.Kind {
		case KindEntity:
			if !contains(p, "EMPTY list") || !contains(p, "VERBATIM") {
				t.Errorf("%s: entity prompt must require verbatim copying and permit empty", tp.ID)
			}
		case KindStructure:
			if !contains(p, "does not state") {
				t.Errorf("%s: structure prompt must permit absent fields", tp.ID)
			}
		case KindMultiLabel:
			if !contains(p, "empty list is valid") {
				t.Errorf("%s: multi-label prompt must permit an empty selection", tp.ID)
			}
		}
	}
}

func contains(h, n string) bool { return len(n) > 0 && len(h) >= len(n) && indexOfStr(h, n) >= 0 }

func indexOfStr(h, n string) int {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return i
		}
	}
	return -1
}
