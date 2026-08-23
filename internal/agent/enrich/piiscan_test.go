package enrich

import (
	"strings"
	"testing"
)

// Synthetic fixtures. Constructed, not collected: the card is an invented
// ascending account body under a real brand IIN with the Luhn check digit
// computed for it, and the SSN is a descending-digit value that satisfies every
// SSA structural rule. Both are therefore valid in shape while belonging to no
// one — and neither appears on any published test/example list, which matters
// because everything on those lists is suppressed by the sidecar's well-known
// gate, so a positive test cannot use one.
const (
	fxCard  = "4539871234567895"
	fxSSN   = "321-54-9876"
	fxEmail = "dana.rivers@northwind-logistics.co.uk"
)

// scanOf builds a PIIScanner that reports the given (label, value) pairs
// wherever they occur in the scanned text — the shape the sidecar's /pii
// answers in: type + offsets + score, never the matched value.
func scanOf(pairs ...[2]string) PIIScanner {
	return func(text string) (PIIResult, bool) {
		var spans []Entity
		for _, p := range pairs {
			if i := strings.Index(text, p[1]); i >= 0 {
				spans = append(spans, Entity{Label: p[0], Start: i, End: i + len(p[1]), Confidence: 0.85})
			}
		}
		return PIIResult{Spans: spans}, true
	}
}

// truncatedScan answers like scanOf but reports that it could not read the
// whole input.
func truncatedScan(pairs ...[2]string) PIIScanner {
	inner := scanOf(pairs...)
	return func(text string) (PIIResult, bool) {
		r, ok := inner(text)
		r.Truncated = true
		return r, ok
	}
}

// downScan is the unreachable service: the call is made and fails.
func downScan(string) (PIIResult, bool) { return PIIResult{}, false }

func sensitivityOf(t *testing.T, text string, m Model, scan PIIScanner) (string, []Entity) {
	t.Helper()
	out, err := SensitivityExtractor{Scan: scan}.Run(NewJobContext(text, "claude_code", Meta{}, m))
	if err != nil {
		t.Fatal(err)
	}
	return out["sensitivity"].(Labeled).Value, out["sensitivity_spans"].([]Entity)
}

func degradedOf(t *testing.T, text string, m Model, scan PIIScanner) bool {
	t.Helper()
	e := SensitivityExtractor{Scan: scan}
	ctx := NewJobContext(text, "claude_code", Meta{}, m)
	out, err := e.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return e.Degraded(ctx, out)
}

// The headline: an ssn span from the PII backend rolls up to phi, with no model
// anywhere in the picture. Before the sidecar's /pii existed the two highest
// classes were reachable only through GLiNER2's NER.
func TestScannerSSNRollsUpToPHI(t *testing.T) {
	text := "update the record, ssn " + fxSSN + ", in billing"
	got, spans := sensitivityOf(t, text, nil, scanOf([2]string{"ssn", fxSSN}))
	if got != "phi" {
		t.Fatalf("sensitivity = %q, want phi", got)
	}
	if len(spans) != 1 || spans[0].Label != "ssn" {
		t.Fatalf("spans = %+v, want one ssn span", spans)
	}
	if spans[0].Text != "" || spans[0].Masked == "" {
		t.Fatalf("span %+v must carry a masked hint and no raw text", spans[0])
	}
}

func TestScannerCardRollsUpToPCI(t *testing.T) {
	if got, _ := sensitivityOf(t, "charge card "+fxCard+" now", nil,
		scanOf([2]string{"credit_card", fxCard})); got != "pci" {
		t.Fatalf("sensitivity = %q, want pci", got)
	}
}

func TestScannerEmailRollsUpToPII(t *testing.T) {
	if got, _ := sensitivityOf(t, "invoice to "+fxEmail+" today", nil,
		scanOf([2]string{"email", fxEmail})); got != "pii" {
		t.Fatalf("sensitivity = %q, want pii", got)
	}
}

// The rollup is a severity order, not last-writer-wins.
func TestRollupPicksHigherSeverity(t *testing.T) {
	text := "card " + fxCard + " and ssn " + fxSSN + " and mail " + fxEmail
	got, spans := sensitivityOf(t, text, nil,
		scanOf([2]string{"credit_card", fxCard}, [2]string{"ssn", fxSSN}, [2]string{"email", fxEmail}))
	if got != "phi" {
		t.Fatalf("sensitivity = %q, want phi (ssn outranks credit_card and email)", got)
	}
	if len(spans) != 3 {
		t.Fatalf("spans = %+v, want one per entity", spans)
	}
}

// Credentials are the layer that needs NOTHING: no model, no sidecar, no
// network. It must keep working when both backends are absent.
func TestCredentialDetectionNeedsNoBackendAtAll(t *testing.T) {
	got, spans := sensitivityOf(t, "here's the token ghp_16C7e42F292c6912E7710c838347Ae178B4a", nil, nil)
	if got != "secrets" {
		t.Fatalf("sensitivity = %q, want secrets from the pure-Go credential layer", got)
	}
	if len(spans) != 1 || spans[0].Label != "api_key" {
		t.Fatalf("spans = %+v, want one masked api_key span", spans)
	}
}

// Masking is the privacy invariant: a span carries coordinates and a redacted
// hint, never the value. The sidecar returns offsets only, so the Go side is
// the only place the value is ever resolved — and it resolves it to a mask.
func TestSpansAreMaskedNotRaw(t *testing.T) {
	text := "card " + fxCard + " ssn " + fxSSN + " mail " + fxEmail
	_, spans := sensitivityOf(t, text, nil,
		scanOf([2]string{"credit_card", fxCard}, [2]string{"ssn", fxSSN}, [2]string{"email", fxEmail}))
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

// --- the well-known gate, on the NER path -----------------------------------

// exampleValueModel is a NER that dutifully reports the documentation values as
// entities — which is what GLiNER2 does, since they are perfectly shaped.
type exampleValueModel struct{ emptyModel }

func (exampleValueModel) Entities(text string, _ map[string]string) []Entity {
	var ents []Entity
	for _, kv := range [][2]string{
		{"ssn", "123-45-6789"},
		{"credit_card", "4111 1111 1111 1111"},
		{"email", "user@example.com"},
	} {
		if i := strings.Index(text, kv[1]); i >= 0 {
			ents = append(ents, Entity{Label: kv[0], Text: kv[1], Start: i, End: i + len(kv[1]), Confidence: 1})
		}
	}
	return ents
}

// The trap this facet cannot ship without. A developer transcript is saturated
// with published values, and the NER reports every one of them as a flawless
// entity. The gate now lives in ONE place — the sidecar's scan, which drops
// them — so the NER's pattern-type findings must be corroborated there before
// they can be published. An empty scan means every one of these is suppressed.
func TestPublishedExampleValuesNeverFireViaTheNER(t *testing.T) {
	text := "test with 4111 1111 1111 1111, ssn 123-45-6789, mail user@example.com"
	// The real /pii returns nothing here: app/wellknown.py suppresses all three.
	got, spans := sensitivityOf(t, text, exampleValueModel{}, scanOf())
	if got != "none" {
		t.Fatalf("sensitivity = %q, want none: every value here is a published example", got)
	}
	if len(spans) != 0 {
		t.Fatalf("spans = %+v, want none", spans)
	}
}

// With the scan unreachable there is no gate at all, so an uncorroborated
// pattern-type entity must be dropped rather than published ungated — the
// documentation-constant flood is exactly what the gate exists to stop.
func TestPatternTypesAreDroppedWhenTheScanIsUnavailable(t *testing.T) {
	text := "test with 4111 1111 1111 1111, ssn 123-45-6789, mail user@example.com"
	got, spans := sensitivityOf(t, text, exampleValueModel{}, downScan)
	if got != "none" || len(spans) != 0 {
		t.Fatalf("sensitivity = %q spans = %+v; an ungated NER pattern match must not be published", got, spans)
	}
}

// nerPersonModel reports the type the scan's own NER is weakest at and which
// no pattern can gate: a person name.
type nerPersonModel struct {
	emptyModel
	needle string
}

func (m nerPersonModel) Entities(text string, _ map[string]string) []Entity {
	i := strings.Index(text, m.needle)
	if i < 0 {
		return nil
	}
	return []Entity{{Label: "person", Text: m.needle, Start: i, End: i + len(m.needle), Confidence: 0.9}}
}

// person/address are the NER's own contribution: they carry no pattern for the
// scan to corroborate, and the published-value lists never covered them.
func TestNERPersonNeedsNoCorroboration(t *testing.T) {
	m := nerPersonModel{needle: "Marguerite Vandenberg"}
	got, spans := sensitivityOf(t, "ask Marguerite Vandenberg to review", m, scanOf())
	if got != "pii" {
		t.Fatalf("sensitivity = %q, want pii", got)
	}
	if len(spans) != 1 || spans[0].Label != "person" {
		t.Fatalf("spans = %+v, want one person span", spans)
	}
}

// ...but a "person" with no letters in it is a mislabelled numeric constant,
// which is how the textbook SSN used to reach the wire as pii. A name has
// letters; an order id does not.
func TestNERPersonWithoutLettersIsDropped(t *testing.T) {
	m := nerPersonModel{needle: "123-45-6789"}
	got, spans := sensitivityOf(t, "order 123-45-6789 shipped", m, scanOf())
	if got != "none" || len(spans) != 0 {
		t.Fatalf("sensitivity = %q spans = %+v; a digits-only person is not a name", got, spans)
	}
}

// nerSSNModel reports the SAME span the scan finds, so the union must not
// publish it twice.
type nerSSNModel struct{ emptyModel }

func (nerSSNModel) Entities(text string, _ map[string]string) []Entity {
	i := strings.Index(text, fxSSN)
	if i < 0 {
		return nil
	}
	return []Entity{{Label: "ssn", Text: fxSSN, Start: i, End: i + len(fxSSN), Confidence: 0.9}}
}

func TestNERAndScanSpansDoNotDuplicate(t *testing.T) {
	got, spans := sensitivityOf(t, "ssn "+fxSSN+" on file", nerSSNModel{}, scanOf([2]string{"ssn", fxSSN}))
	if got != "phi" {
		t.Fatalf("sensitivity = %q, want phi", got)
	}
	if len(spans) != 1 {
		t.Fatalf("spans = %+v, want a single ssn span (the NER and the scan found the same one)", spans)
	}
}

// A placeholder that the scan reported is still a placeholder: the sidecar's
// gate knows published constants, not this repo's redaction conventions.
func TestPlaceholderValuesFromTheScanAreDropped(t *testing.T) {
	const ph = "<REDACTED>"
	got, spans := sensitivityOf(t, "the key is "+ph+" now", nil, scanOf([2]string{"person", ph}))
	if got != "none" || len(spans) != 0 {
		t.Fatalf("sensitivity = %q spans = %+v; a redaction marker is not leaked data", got, spans)
	}
}

// --- honesty about availability ---------------------------------------------

// A whole scan covers every entity type in the vocabulary, so the answer is
// complete whether or not a model was present.
func TestWholeScanIsNotDegraded(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    Model
	}{{"no model", nil}, {"with model", emptyModel{}}} {
		if degradedOf(t, "bump the timeout to 30s", tc.m, scanOf()) {
			t.Errorf("%s: the scan ran whole; nothing is missing", tc.name)
		}
	}
}

// The regression this task exists to prevent: with the service unreachable, PII
// detection is genuinely absent, and sensitivity:"none" is then a confident
// negative from a check that never ran.
func TestUnavailableScanDegradesTheFacet(t *testing.T) {
	for _, tc := range []struct {
		name string
		scan PIIScanner
	}{
		{"no scanner wired", nil},
		{"scan call failed", downScan},
	} {
		if !degradedOf(t, "bump the timeout to 30s", emptyModel{}, tc.scan) {
			t.Errorf("%s: sensitivity must be named degraded", tc.name)
		}
	}
}

// A truncated scan read part of the input, so anything in the tail is
// undetected — the same "may be understated" the absent case carries.
func TestTruncatedScanDegradesTheFacet(t *testing.T) {
	if !degradedOf(t, "bump the timeout to 30s", nil, truncatedScan()) {
		t.Fatal("a truncated scan is possible under-detection and must be marked degraded")
	}
}

// ...unless the answer already reached the ceiling of the severity order.
// Nothing the missing evidence could add would raise phi, and a marker that
// fires on answers it cannot change stops being read on the ones it can.
func TestCeilingResultIsNotQualified(t *testing.T) {
	text := "ssn " + fxSSN + " on file"
	if degradedOf(t, text, nil, truncatedScan([2]string{"ssn", fxSSN})) {
		t.Fatal("phi is the top class; nothing the unscanned tail holds could raise it")
	}
}
