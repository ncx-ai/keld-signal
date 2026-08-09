package llmstudy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// GalleryGold is one hand-authored evaluation row.
//
// Unlike everything else measured in this study, this is GROUND TRUTH, not
// cross-model agreement — so it can say whether an answer is right, not merely
// whether two models concur. It exists because the mined corpus is one engineer's
// coding transcripts and contains almost no vendor, billing or campaign material:
// those templates cannot be evaluated on it at all.
//
// Exactly one of Entities / Fields / Label / Labels is populated, per Kind.
type GalleryGold struct {
	ID       string              `json:"id"`
	Template string              `json:"template"`
	Kind     string              `json:"kind"`
	Text     string              `json:"text"`
	Entities map[string][]string `json:"entities,omitempty"` // entity: type -> expected verbatim spans
	Fields   map[string]string   `json:"fields,omitempty"`   // structure: field -> expected value ("" = absent)
	Label    string              `json:"label,omitempty"`    // single_label
	Labels   []string            `json:"labels,omitempty"`   // multi_label
	// Note records why a row exists — especially for negatives and decoys, where the
	// reasoning behind the gold answer is the part worth preserving.
	Note string `json:"note,omitempty"`
}

// LoadGalleryGold reads the gold set, skipping blank lines.
func LoadGalleryGold(path string) ([]GalleryGold, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []GalleryGold
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<16), 1<<20)
	line := 0
	for sc.Scan() {
		line++
		s := strings.TrimSpace(sc.Text())
		if s == "" {
			continue
		}
		var g GalleryGold
		if err := json.Unmarshal([]byte(s), &g); err != nil {
			return nil, fmt.Errorf("line %d: %w", line, err)
		}
		out = append(out, g)
	}
	return out, sc.Err()
}

// ValidateGalleryGold checks the gold set is internally coherent before it is used
// to judge anything. A typo here would otherwise surface as a model failure.
//
// The substring check is the important one: entity gold and non-empty structure gold
// must appear verbatim in the text, because that is what the model is instructed to
// produce and what a verification gate would enforce at publish time.
func ValidateGalleryGold(rows []GalleryGold) []string {
	var problems []string
	seen := map[string]bool{}
	for _, g := range rows {
		if seen[g.ID] {
			problems = append(problems, g.ID+": duplicate id")
		}
		seen[g.ID] = true

		t, ok := GalleryByID(g.Template)
		if !ok {
			problems = append(problems, g.ID+": unknown template "+g.Template)
			continue
		}
		if string(t.Kind) != g.Kind {
			problems = append(problems, fmt.Sprintf("%s: kind %q but template %s is %q",
				g.ID, g.Kind, t.ID, t.Kind))
		}
		low := strings.ToLower(g.Text)

		switch t.Kind {
		case KindEntity:
			known := map[string]bool{}
			for _, ty := range t.Types {
				known[ty.Name] = true
			}
			for name, spans := range g.Entities {
				if !known[name] {
					problems = append(problems, g.ID+": unknown entity type "+name)
				}
				for _, s := range spans {
					if !strings.Contains(low, strings.ToLower(s)) {
						problems = append(problems,
							fmt.Sprintf("%s: span %q is not a substring of the text", g.ID, s))
					}
				}
			}
			for _, ty := range t.Types {
				if _, ok := g.Entities[ty.Name]; !ok {
					problems = append(problems,
						fmt.Sprintf("%s: entity type %q missing — state [] explicitly so negatives are deliberate",
							g.ID, ty.Name))
				}
			}
		case KindStructure:
			known := map[string]bool{}
			for _, f := range t.Fields {
				known[f.Name] = true
			}
			for name, val := range g.Fields {
				if !known[name] {
					problems = append(problems, g.ID+": unknown field "+name)
				}
				if val == "" {
					continue // deliberately absent
				}
				// A list field's gold is comma-joined; check each part.
				for _, part := range strings.Split(val, ", ") {
					if strings.Contains(low, strings.ToLower(part)) {
						continue
					}
					// Allowed exception: `choice` fields normalise (prod -> production).
					if isChoiceField(t, name) {
						continue
					}
					problems = append(problems,
						fmt.Sprintf("%s: field %s value %q is not in the text", g.ID, name, part))
				}
			}
			for _, f := range t.Fields {
				if _, ok := g.Fields[f.Name]; !ok {
					problems = append(problems,
						fmt.Sprintf("%s: field %q missing — state \"\" explicitly", g.ID, f.Name))
				}
			}
		case KindSingleLabel:
			if !validValue(t, g.Label) {
				problems = append(problems, g.ID+": label not in template values: "+g.Label)
			}
		case KindMultiLabel:
			for _, l := range g.Labels {
				if !validValue(t, l) {
					problems = append(problems, g.ID+": label not in template values: "+l)
				}
			}
		}
	}
	return problems
}

func isChoiceField(t GalleryTemplate, name string) bool {
	for _, f := range t.Fields {
		if f.Name == name {
			return f.DType == "choice"
		}
	}
	return false
}

func validValue(t GalleryTemplate, id string) bool {
	for _, v := range t.Values {
		if v.ID == id {
			return true
		}
	}
	return false
}
