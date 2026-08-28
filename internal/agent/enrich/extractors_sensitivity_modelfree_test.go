package enrich

import (
	"reflect"
	"strings"
	"testing"
)

// The sensitivity facet must not consult GLiNER2 at all. Its evidence is the
// gitleaks credential layer (pure Go) and the presidio scan behind the
// sidecar's /pii — neither of which is the inference model. These tests pin
// that as an EQUIVALENCE rather than as two separate behaviours: whatever a
// Model would have said, the published output is byte-identical to the run
// that had no Model at all.

// loudModel is the most talkative backend imaginable: it reports an entity for
// every sensitive label it can find in the text and answers every
// classification task confidently. It exists to be ignored. Any path from
// ctx.Model to the output makes the equivalence below fail.
type loudModel struct{ needles map[string]string }

func newLoudModel() loudModel {
	return loudModel{needles: map[string]string{
		"ssn":         fxSSN,
		"credit_card": fxCard,
		"email":       fxEmail,
		"person":      "Marguerite Vandenberg",
		"address":     "14 Kingsway Terrace",
		"api_key":     "ghp_16C7e42F292c6912E7710c838347Ae178B4a",
		"phone":       "(415) 682-4470",
	}}
}

func (m loudModel) Entities(text string, _ map[string]string) []Entity {
	var out []Entity
	for label, needle := range m.needles {
		if i := strings.Index(text, needle); i >= 0 {
			out = append(out, Entity{
				Label: label, Text: needle, Start: i, End: i + len(needle), Confidence: 1,
			})
		}
	}
	// Plus the failure mode the NER was worst at: the textbook constants, which
	// it reports as flawless entities because they are flawless in shape.
	for _, kv := range [][2]string{
		{"ssn", "123-45-6789"},
		{"credit_card", "4111 1111 1111 1111"},
		{"email", "user@example.com"},
	} {
		if i := strings.Index(text, kv[1]); i >= 0 {
			out = append(out, Entity{Label: kv[0], Text: kv[1], Start: i, End: i + len(kv[1]), Confidence: 1})
		}
	}
	return out
}

func (loudModel) Classify(_ string, tasks map[string][]string) map[string][]Ranked {
	out := map[string][]Ranked{}
	for task := range tasks {
		out[task] = []Ranked{{Label: "pci", Confidence: 0.99}}
	}
	return out
}

func (m loudModel) Extract(text string, labels map[string]string, tasks map[string][]string) ExtractResult {
	return ExtractResult{Entities: m.Entities(text, labels), Results: m.Classify(text, tasks)}
}

// TestSensitivityIsIdenticalWithAndWithoutAModel is the requirement itself. Not
// "works without a model" and separately "works with one" — the two runs must
// agree exactly, which is the only assertion that a Model cannot influence the
// answer.
func TestSensitivityIsIdenticalWithAndWithoutAModel(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		scan PIIScanner
	}{
		{"nothing sensitive", "bump the retry timeout to 30s", scanOf()},
		{"ssn via the scan", "record ssn " + fxSSN + " in billing", scanOf([2]string{"ssn", fxSSN})},
		{"card via the scan", "charge " + fxCard + " now", scanOf([2]string{"credit_card", fxCard})},
		{"person via the scan", "ask Marguerite Vandenberg to review", scanOf([2]string{"person", "Marguerite Vandenberg"})},
		{"address via the scan", "ship to 14 Kingsway Terrace today", scanOf([2]string{"address", "14 Kingsway Terrace"})},
		// The discriminating rows. person/address are the two types the NER was
		// admitted UNCORROBORATED for, so they are the only place a Model could
		// still move the answer: the scan reports nothing and the model reports
		// a name. If these agree, no evidence path from ctx.Model survives.
		{"person the scan did not report", "ask Marguerite Vandenberg to review", scanOf()},
		{"address the scan did not report", "ship to 14 Kingsway Terrace today", scanOf()},
		{"person with the scan unavailable", "ask Marguerite Vandenberg to review", downScan},
		{"credential, no scan needed", "token ghp_16C7e42F292c6912E7710c838347Ae178B4a", scanOf()},
		{"published examples the scan suppresses", "try 4111 1111 1111 1111, ssn 123-45-6789, mail user@example.com", scanOf()},
		{"scan unavailable", "record ssn " + fxSSN + " in billing", downScan},
		{"no scanner wired", "record ssn " + fxSSN + " in billing", nil},
		{"truncated scan", "record ssn " + fxSSN + " in billing", truncatedScan([2]string{"ssn", fxSSN})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := SensitivityExtractor{Scan: tc.scan}

			withCtx := NewJobContext(tc.text, "claude_code", Meta{}, newLoudModel())
			with, err := e.Run(withCtx)
			if err != nil {
				t.Fatal(err)
			}
			withoutCtx := NewJobContext(tc.text, "claude_code", Meta{}, nil)
			without, err := e.Run(withoutCtx)
			if err != nil {
				t.Fatal(err)
			}

			if !reflect.DeepEqual(with, without) {
				t.Fatalf("a Model changed the answer:\n with model: %+v\n  no model: %+v", with, without)
			}
			if e.Degraded(withCtx, with) != e.Degraded(withoutCtx, without) {
				t.Fatalf("a Model changed the degraded marker: with=%v without=%v",
					e.Degraded(withCtx, with), e.Degraded(withoutCtx, without))
			}
		})
	}
}
