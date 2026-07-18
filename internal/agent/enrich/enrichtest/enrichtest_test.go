package enrichtest

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

func findEntity(es []enrich.Entity, label string) (enrich.Entity, bool) {
	for _, e := range es {
		if e.Label == label {
			return e, true
		}
	}
	return enrich.Entity{}, false
}

func TestFakeDetectsEmailAndKey(t *testing.T) {
	m := NewFake()
	text := "email me at jane@acme.com with key sk-live-ABCDEF0123456789"
	es := m.Entities(text, enrich.SensitiveEntityLabels)
	em, ok := findEntity(es, "email")
	if !ok || text[em.Start:em.End] != "jane@acme.com" {
		t.Fatalf("email span wrong: %+v", em)
	}
	if _, ok := findEntity(es, "api_key"); !ok {
		t.Fatalf("expected api_key entity in %+v", es)
	}
}

func TestFakeClassifyCodegen(t *testing.T) {
	m := NewFake()
	res := m.Classify("Write a Go function to parse JSON", map[string][]string{"task_type": enrich.TaskTypes})
	ranked := res["task_type"]
	if len(ranked) == 0 || ranked[0].Label != "code_generation" {
		t.Fatalf("top task_type = %+v, want code_generation", ranked)
	}
}

func TestFakeClassifyFallsBackToGeneral(t *testing.T) {
	m := NewFake()
	res := m.Classify("zzzzz", map[string][]string{"task_type": enrich.TaskTypes})
	if res["task_type"][0].Label != "general" {
		t.Fatalf("unmatched should be 'general', got %+v", res["task_type"])
	}
}

func TestFakeCreditCardLuhnTruePositive(t *testing.T) {
	m := NewFake()
	text := "please charge 4111 1111 1111 1111 for this order"
	es := m.Entities(text, enrich.SensitiveEntityLabels)
	if _, ok := findEntity(es, "credit_card"); !ok {
		t.Fatalf("expected credit_card entity for valid Luhn number in %q, got %+v", text, es)
	}
}

func TestFakeCreditCardRejectsNonCardDigits(t *testing.T) {
	m := NewFake()
	text := "timestamp 20240101120000 logged"
	es := m.Entities(text, enrich.SensitiveEntityLabels)
	if e, ok := findEntity(es, "credit_card"); ok {
		t.Fatalf("expected no credit_card entity for timestamp, got %+v", e)
	}
}

func TestFakePhoneMatchesRealNumber(t *testing.T) {
	m := NewFake()
	text := "call 555-123-4567 for details"
	es := m.Entities(text, enrich.SensitiveEntityLabels)
	if _, ok := findEntity(es, "phone"); !ok {
		t.Fatalf("expected phone entity in %q, got %+v", text, es)
	}
}

func TestFakeAbstainsOnUnknownTask(t *testing.T) {
	m := NewFake()
	res := m.Classify("write a function", map[string][]string{"personal": {"a work task", "personal activity"}})
	ranked := res["personal"]
	if len(ranked) != 1 || ranked[0].Label != "" || ranked[0].Confidence != 0 {
		t.Fatalf("expected a single abstaining Ranked{Label:\"\", Confidence:0} for a task with no keyword priors, got %+v", ranked)
	}

	// A known task (has a keyword table) must still classify normally.
	res = m.Classify("write a function", map[string][]string{"task_type": enrich.TaskTypes})
	if got := res["task_type"]; len(got) == 0 || got[0].Label == "" {
		t.Fatalf("expected task_type to still classify (known task), got %+v", got)
	}
}

func TestFakePhoneIgnoresStreetAddress(t *testing.T) {
	m := NewFake()
	text := "123 Main St Apt 4"
	es := m.Entities(text, enrich.SensitiveEntityLabels)
	if e, ok := findEntity(es, "phone"); ok {
		t.Fatalf("expected no phone entity for street address, got %+v", e)
	}
}
