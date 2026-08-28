package llmstudy

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// GalleryAnswer is one model response to one gold row.
type GalleryAnswer struct {
	ID        string              `json:"id"`
	Template  string              `json:"template"`
	Entities  map[string][]string `json:"entities,omitempty"`
	Dropped   map[string][]string `json:"dropped,omitempty"` // failed the verbatim check
	Fields    map[string]string   `json:"fields,omitempty"`
	Label     string              `json:"label,omitempty"`
	Labels    []string            `json:"labels,omitempty"`
	LatencyMS int64               `json:"latency_ms"`
	Valid     bool                `json:"valid"`
	Err       string              `json:"err,omitempty"`
}

// RunGallery answers one gold row.
//
// Entity spans are passed through the same verbatim gate the real pipeline would
// need: a span that is not a substring of the input is DROPPED, not scored. That
// keeps hallucinated spans out of the numbers rather than letting them count as
// wrong answers — they are not answers at all, and at publish time they would be
// discarded before anyone saw them.
func (l *Llama) RunGallery(g GalleryGold) (a GalleryAnswer) {
	a = GalleryAnswer{ID: g.ID, Template: g.Template}
	start := time.Now()
	defer func() { a.LatencyMS = time.Since(start).Milliseconds() }()

	t, ok := GalleryByID(g.Template)
	if !ok {
		a.Err = "unknown template " + g.Template
		return a
	}
	schema := GallerySchema(t)
	prompt := GalleryPrompt(t, g.Text)

	switch t.Kind {
	case KindEntity:
		var raw map[string][]string
		if err := l.call(prompt, schema, &raw); err != nil {
			a.Err = err.Error()
			return a
		}
		a.Entities, a.Dropped = map[string][]string{}, map[string][]string{}
		for _, ty := range t.Types {
			kept, dropped := VerifyTopics(raw[ty.Name], g.Text)
			a.Entities[ty.Name] = kept
			if len(dropped) > 0 {
				a.Dropped[ty.Name] = dropped
			}
		}
	case KindStructure:
		// `list` fields arrive as arrays; decode loosely then normalise.
		var raw map[string]json.RawMessage
		if err := l.call(prompt, schema, &raw); err != nil {
			a.Err = err.Error()
			return a
		}
		a.Fields = map[string]string{}
		for _, f := range t.Fields {
			a.Fields[f.Name] = normaliseField(raw[f.Name])
		}
	case KindSingleLabel:
		var raw struct {
			Label string `json:"label"`
		}
		if err := l.call(prompt, schema, &raw); err != nil {
			a.Err = err.Error()
			return a
		}
		a.Label = raw.Label
	case KindMultiLabel:
		var raw struct {
			Labels []string `json:"labels"`
		}
		if err := l.call(prompt, schema, &raw); err != nil {
			a.Err = err.Error()
			return a
		}
		a.Labels = raw.Labels
	}
	a.Valid = true
	return a
}

// normaliseField renders a structure field as a comparable string: a list becomes
// ", "-joined in sorted order so ordering differences are not scored as errors.
func normaliseField(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return strings.TrimSpace(s)
	}
	var l []string
	if json.Unmarshal(raw, &l) == nil {
		out := make([]string, 0, len(l))
		for _, v := range l {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		// Case-INSENSITIVE sort: a case-sensitive one puts "LinkedIn" before
		// "email" (uppercase sorts first in ASCII), which made an order-only
		// difference score as a wrong answer.
		sort.Slice(out, func(i, j int) bool {
			return strings.ToLower(out[i]) < strings.ToLower(out[j])
		})
		return strings.Join(out, ", ")
	}
	return ""
}

// Score counts one template's outcomes across gold rows.
type Score struct {
	TP, FP, FN int
	// Exact counts rows where the whole row matched — the number that matters for a
	// structure template, where a half-right object is not usable.
	Exact, Rows  int
	Invalid      int
	Hallucinated int // spans dropped by the verbatim gate
}

func (s Score) Precision() float64 {
	if s.TP+s.FP == 0 {
		return 1 // claimed nothing, was right to
	}
	return float64(s.TP) / float64(s.TP+s.FP)
}

func (s Score) Recall() float64 {
	if s.TP+s.FN == 0 {
		return 1 // nothing to find, found nothing
	}
	return float64(s.TP) / float64(s.TP+s.FN)
}

func (s Score) F1() float64 {
	p, r := s.Precision(), s.Recall()
	if p+r == 0 {
		return 0
	}
	return 2 * p * r / (p + r)
}

func (s Score) ExactRate() float64 {
	if s.Rows == 0 {
		return 0
	}
	return float64(s.Exact) / float64(s.Rows)
}

// normSpan lowercases, trims, and collapses non-alphanumeric runs to single spaces,
// so separator choices are not scored as extraction errors.
//
// Observed need: a model answered "billing-worker" where gold said "billing worker".
// That is the same extraction with a different separator, and counting it as both a
// false positive and a false negative would measure punctuation rather than whether
// the right thing was found.
func normSpan(s string) string {
	var b strings.Builder
	lastSpace := true
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastSpace = false
		default:
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

// ScoreGallery scores answers against gold, per template.
//
// Entity scoring is set-based per type: a predicted span counts as a TP when gold
// contains it, allowing substring containment in either direction so that "Postgres"
// against gold "Postgres 16" is credited rather than punished for boundary choice.
// Boundary disagreements are a real but separate problem from finding the right
// thing, and conflating them would make the numbers unreadable.
func ScoreGallery(gold []GalleryGold, answers map[string]GalleryAnswer) map[string]*Score {
	out := map[string]*Score{}
	get := func(t string) *Score {
		if out[t] == nil {
			out[t] = &Score{}
		}
		return out[t]
	}
	for _, g := range gold {
		s := get(g.Template)
		s.Rows++
		a, ok := answers[g.ID]
		if !ok || !a.Valid {
			s.Invalid++
			continue
		}
		for _, d := range a.Dropped {
			s.Hallucinated += len(d)
		}
		rowExact := true
		switch g.Kind {
		case "entity":
			for ty, want := range g.Entities {
				got := a.Entities[ty]
				matched := map[int]bool{}
				for _, p := range got {
					hit := -1
					for i, w := range want {
						if matched[i] {
							continue
						}
						if spanMatch(p, w) {
							hit = i
							break
						}
					}
					if hit >= 0 {
						matched[hit] = true
						s.TP++
					} else {
						s.FP++
						rowExact = false
					}
				}
				for i := range want {
					if !matched[i] {
						s.FN++
						rowExact = false
					}
				}
			}
		case "structure":
			for f, want := range g.Fields {
				got := a.Fields[f]
				switch {
				case want == "" && got == "":
					// correctly absent; not counted either way
				case want == "" && got != "":
					s.FP++
					rowExact = false
				case want != "" && got == "":
					s.FN++
					rowExact = false
				case spanMatch(got, want):
					s.TP++
				default:
					s.FP++
					s.FN++
					rowExact = false
				}
			}
		case "single_label":
			if a.Label == g.Label {
				s.TP++
			} else {
				s.FP++
				s.FN++
				rowExact = false
			}
		case "multi_label":
			want := map[string]bool{}
			for _, l := range g.Labels {
				want[l] = true
			}
			seen := map[string]bool{}
			for _, l := range a.Labels {
				if seen[l] {
					continue
				}
				seen[l] = true
				if want[l] {
					s.TP++
				} else {
					s.FP++
					rowExact = false
				}
			}
			for l := range want {
				if !seen[l] {
					s.FN++
					rowExact = false
				}
			}
		}
		if rowExact {
			s.Exact++
		}
	}
	return out
}

// spanMatch credits a prediction when it equals gold or either contains the other,
// so a boundary disagreement ("Postgres" vs "Postgres 16") is not scored as a miss.
func spanMatch(got, want string) bool {
	g, w := normSpan(got), normSpan(want)
	if g == "" || w == "" {
		return g == w
	}
	return g == w || strings.Contains(g, w) || strings.Contains(w, g)
}
