package publish

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// AC-7: the wire carries projects + status + meta, and the meta is numbers
// and enums only — assert the marshalled JSON has no field that could hold
// message text (mirrors TestEnrichmentWireShapeCannotCarryAnalysisInternals).
func TestBlockWireCarriesProjects(t *testing.T) {
	b := BlockEnrichment{SchemaVersion: enrich.SchemaVersion}
	b = WithProjects(b,
		[]enrich.ProjectAttribution{{ID: "proj_pay", Confidence: 0.91, Source: "embedding"}},
		enrich.ProjectsAttributed,
		&enrich.AttributionMeta{EmbedMS: 812, VerifyMS: 0, ConceptMS: 140,
			EncoderState: "warm", Verifier: "not_needed"},
		[]enrich.Concept{{Value: "dunning retry", Score: 0.71}})
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"projects":[{"id":"proj_pay"`, `"projects_status":"attributed"`,
		`"embed_ms":812`, `"concept_ms":140`, `"concepts":[{"value":"dunning retry"`} {
		if !strings.Contains(s, want) {
			t.Fatalf("wire body missing %s in %s", want, s)
		}
	}
	if enrich.SchemaVersion != 23 {
		t.Fatalf("SchemaVersion = %d, want 23", enrich.SchemaVersion)
	}
}

// A block that has NOT been through attribution is byte-identical to the
// payload before concepts existed — the same opt-in property Projects has, and
// what lets a machine with attribution off publish an unchanged row.
func TestABlockWithNoAttributionCarriesNoConceptsKey(t *testing.T) {
	raw, err := json.Marshal(BlockEnrichment{SchemaVersion: enrich.SchemaVersion})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "concepts") {
		t.Fatalf("unattributed block carries a concepts key: %s", raw)
	}
}

// ⚠️ CONCEPTS ARE TEXT AND THE SHAPE BOUNDS ARE THE WHOLE PUBLISH ARGUMENT.
// enrich.Concept deliberately sits OUTSIDE the reflection tripwire below, so
// nothing else pins it; this is what holds the two numbers that make a phrase
// publishable rather than a fragment of the conversation.
func TestConceptBoundsAreTheOnesTheArgumentRestsOn(t *testing.T) {
	if enrich.MaxConceptWords != 3 || enrich.MaxConcepts != 8 {
		t.Fatalf("concept bounds moved to words=%d count=%d — that widens what block text can "+
			"cross, so it is a privacy decision, not a tuning change",
			enrich.MaxConceptWords, enrich.MaxConcepts)
	}
	names := structFieldNames(enrich.Concept{})
	if !reflect.DeepEqual(names, []string{"Value", "Score"}) {
		t.Fatalf("Concept fields = %v — a span, an offset or a position here would cross the "+
			"line the phrase bound keeps this side of", names)
	}
}

// The struct must be structurally unable to carry text: every string field of
// ProjectAttribution and AttributionMeta is an id or a closed enum. Guard by
// reflection so a future field addition trips this test.
func TestAttributionShapeHoldsNoText(t *testing.T) {
	// ⚠️ ConceptMS is an integer millisecond timing, admitted on the same footing
	// as EmbedMS/VerifyMS. The concepts THEMSELVES are deliberately not a field
	// of either struct — they are text, and they ride BlockEnrichment.Concepts
	// with their own bounds (see TestConceptBoundsAreTheOnesTheArgumentRestsOn),
	// precisely so this tripwire keeps meaning what it says.
	allowed := map[string]bool{"ID": true, "Confidence": true, "Source": true, "ConceptMS": true,
		"EmbedMS": true, "VerifyMS": true, "PairsVerified": true,
		"EncoderState": true, "Verifier": true, "ModelVersions": true}
	for _, name := range structFieldNames(enrich.ProjectAttribution{}) {
		if !allowed[name] {
			t.Fatalf("new field %q on ProjectAttribution — extend the allowlist only after a privacy review", name)
		}
	}
	for _, name := range structFieldNames(enrich.AttributionMeta{}) {
		if !allowed[name] {
			t.Fatalf("new field %q on AttributionMeta — extend the allowlist only after a privacy review", name)
		}
	}
}

func structFieldNames(v any) []string {
	t := reflect.TypeOf(v)
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		out = append(out, t.Field(i).Name)
	}
	return out
}
