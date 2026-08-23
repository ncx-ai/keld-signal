package enrich

import (
	"strings"
	"testing"
)

// Synthetic fixtures. Constructed, not collected: the cards are an invented
// ascending account body under a real brand IIN with the Luhn check digit
// computed for it, and the SSN is a descending-digit value that satisfies every
// SSA structural rule. Both are therefore valid in shape while belonging to no
// one — and neither appears on any published test/example list, which matters
// because everything on those lists is excluded by piidetect's well-known gate,
// so a positive test cannot use one.
const (
	fxCard  = "4539871234567895"
	fxSSN   = "321-54-9876"
	fxEmail = "dana.rivers@northwind-logistics.co.uk"
)

func sensitivityOf(t *testing.T, text string, m Model) (string, []Entity) {
	t.Helper()
	out, err := SensitivityExtractor{}.Run(NewJobContext(text, "claude_code", Meta{}, m))
	if err != nil {
		t.Fatal(err)
	}
	return out["sensitivity"].(Labeled).Value, out["sensitivity_spans"].([]Entity)
}

// The motivating regression: with ml_backend:"deterministic" there is no model
// at all, and before piidetect an SSN in the prompt produced sensitivity:"none"
// — the two highest classes were structurally unreachable.
func TestDeterministicModeReachesPHI(t *testing.T) {
	got, spans := sensitivityOf(t, "update the record, ssn "+fxSSN+", in billing", nil)
	if got != "phi" {
		t.Fatalf("sensitivity = %q, want phi with no model present", got)
	}
	if len(spans) != 1 || spans[0].Label != "ssn" {
		t.Fatalf("spans = %+v, want one ssn span", spans)
	}
}

func TestDeterministicModeReachesPCI(t *testing.T) {
	if got, _ := sensitivityOf(t, "charge card "+fxCard+" for the renewal", nil); got != "pci" {
		t.Fatalf("sensitivity = %q, want pci with no model present", got)
	}
}

func TestDeterministicModeReachesPII(t *testing.T) {
	if got, _ := sensitivityOf(t, "send the invoice to "+fxEmail+" today", nil); got != "pii" {
		t.Fatalf("sensitivity = %q, want pii with no model present", got)
	}
}

// The rollup is a severity order, not a last-writer-wins: a card AND an SSN in
// one prompt must publish the higher class.
func TestRollupPicksHigherSeverity(t *testing.T) {
	text := "card " + fxCard + " and ssn " + fxSSN + " and mail " + fxEmail
	got, spans := sensitivityOf(t, text, nil)
	if got != "phi" {
		t.Fatalf("sensitivity = %q, want phi (ssn outranks credit_card and email)", got)
	}
	if len(spans) != 3 {
		t.Fatalf("spans = %+v, want one per entity", spans)
	}
}

// The trap. A transcript full of documentation values must stay quiet, in
// deterministic mode and with a model alike — the gate is applied to the NER's
// entities too, not only to the deterministic layer's.
func TestPublishedExampleValuesNeverFire(t *testing.T) {
	text := "test with 4111 1111 1111 1111, ssn 123-45-6789, mail user@example.com"
	for _, tc := range []struct {
		name string
		m    Model
	}{
		{"deterministic", nil},
		{"with NER", exampleValueModel{}},
	} {
		got, spans := sensitivityOf(t, text, tc.m)
		if got != "none" {
			t.Errorf("%s: sensitivity = %q, want none: every value here is a published example", tc.name, got)
		}
		if len(spans) != 0 {
			t.Errorf("%s: spans = %+v, want none", tc.name, spans)
		}
	}
}

// exampleValueModel is a NER that dutifully reports the documentation values as
// entities — which is what GLiNER2 does, since they are perfectly shaped.
type exampleValueModel struct{ emptyModel }

func (exampleValueModel) Entities(text string, _ map[string]string) []Entity {
	var ents []Entity
	for label, needle := range map[string]string{
		"ssn":         "123-45-6789",
		"credit_card": "4111 1111 1111 1111",
		"email":       "user@example.com",
	} {
		if i := strings.Index(text, needle); i >= 0 {
			ents = append(ents, Entity{Label: label, Text: needle, Start: i, End: i + len(needle), Confidence: 1})
		}
	}
	return ents
}

// nerSSNModel reports the SAME span the deterministic layer finds, so the union
// must not publish it twice.
type nerSSNModel struct{ emptyModel }

func (nerSSNModel) Entities(text string, _ map[string]string) []Entity {
	i := strings.Index(text, fxSSN)
	if i < 0 {
		return nil
	}
	return []Entity{{Label: "ssn", Text: fxSSN, Start: i, End: i + len(fxSSN), Confidence: 0.9}}
}

func TestNERAndDeterministicSpansDoNotDuplicate(t *testing.T) {
	got, spans := sensitivityOf(t, "ssn "+fxSSN+" on file", nerSSNModel{})
	if got != "phi" {
		t.Fatalf("sensitivity = %q, want phi", got)
	}
	if len(spans) != 1 {
		t.Fatalf("spans = %+v, want a single ssn span (NER and piidetect found the same one)", spans)
	}
}

// Masking is the privacy invariant: a span carries coordinates and a redacted
// hint, never the value.
func TestSpansAreMaskedNotRaw(t *testing.T) {
	text := "card " + fxCard + " ssn " + fxSSN + " mail " + fxEmail
	_, spans := sensitivityOf(t, text, nil)
	if len(spans) == 0 {
		t.Fatal("premise: expected spans")
	}
	for _, s := range spans {
		if s.Text != "" {
			t.Errorf("span %+v carries raw text", s)
		}
		if s.Masked == "" {
			t.Errorf("span %+v has no masked hint", s)
		}
		for _, raw := range []string{fxCard, fxSSN, fxEmail} {
			if strings.Contains(s.Masked, raw) {
				t.Errorf("masked hint %q contains the raw value", s.Masked)
			}
		}
	}
}
