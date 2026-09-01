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
		&enrich.AttributionMeta{EmbedMS: 812, VerifyMS: 0, EncoderState: "warm", Verifier: "not_needed"})
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for _, want := range []string{`"projects":[{"id":"proj_pay"`, `"projects_status":"attributed"`, `"embed_ms":812`} {
		if !strings.Contains(s, want) {
			t.Fatalf("wire body missing %s in %s", want, s)
		}
	}
	if enrich.SchemaVersion != 22 {
		t.Fatalf("SchemaVersion = %d, want 22", enrich.SchemaVersion)
	}
}

// The struct must be structurally unable to carry text: every string field of
// ProjectAttribution and AttributionMeta is an id or a closed enum. Guard by
// reflection so a future field addition trips this test.
func TestAttributionShapeHoldsNoText(t *testing.T) {
	allowed := map[string]bool{"ID": true, "Confidence": true, "Source": true,
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
