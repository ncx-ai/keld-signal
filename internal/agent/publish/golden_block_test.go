package publish

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

const goldenBlockPath = "../../../scripts/testdata/golden_block_with_projects.json"

// The payload scripts/contract_test_atlas.sh POSTs to a stock Atlas is the
// CURRENT wire, not a hand-written approximation of it. It drifted once
// (schema 22, a `source:"metadata"` no producer can emit), so it is now pinned:
// the file must round-trip through BlockEnrichment byte for byte, carry the
// current schema, and name the per-group decision. Regenerate deliberately
// with KELD_UPDATE_GOLDEN=1 go test ./internal/agent/publish/ -run Golden.
func TestGoldenBlockIsTheCurrentWire(t *testing.T) {
	raw, err := os.ReadFile(goldenBlockPath)
	if err != nil {
		t.Fatal(err)
	}
	var b BlockEnrichment
	if err := json.Unmarshal(raw, &b); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("KELD_UPDATE_GOLDEN") == "1" {
		b.SchemaVersion = enrich.SchemaVersion
		b.Projects = []enrich.ProjectAttribution{
			{ID: "contract_products:pay", Confidence: 0.91, Source: "embedding"},
			{ID: "contract_features:infra", Confidence: 0.68, Source: "embedding"},
		}
		b.ProjectsStatus = enrich.ProjectsAttributed
		b.Attribution = &enrich.AttributionMeta{EmbedMS: 812, VerifyMS: 0, ConceptMS: 140,
			EncoderState: "warm", Verifier: "not_needed", Centred: true, BackgroundN: 320,
			ModelVersions: map[string]string{
				"encoder": "qwen3-embedding-0.6b", "verifier": "gemma-4-e2b-q4km",
				"scoring": "block-mean-centred-perstream-v1", "decision": "per-group-margin-v1",
			}}
		out, err := json.MarshalIndent(b, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenBlockPath, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		raw = append(out, '\n')
	}
	out, err := json.MarshalIndent(b, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.TrimSpace(out), bytes.TrimSpace(raw)) {
		t.Fatalf("the golden is not what BlockEnrichment marshals — regenerate it (see the comment)")
	}
	if b.SchemaVersion != enrich.SchemaVersion {
		t.Fatalf("golden schema_version = %d, want %d", b.SchemaVersion, enrich.SchemaVersion)
	}
	if b.Attribution == nil || b.Attribution.ModelVersions["decision"] != "per-group-margin-v1" {
		t.Fatalf("the golden must name the per-group decision: %+v", b.Attribution)
	}
	if strings.Contains(string(raw), `"metadata"`) {
		t.Fatal(`"metadata" is not a producible source and must not be in the golden`)
	}
}

// AC-6. A block in two groups carries every id the sidecar assigned, in the
// sidecar's order (highest confidence first), under the unchanged keys.
func TestAMultiGroupBlockKeepsEveryIDInOrder(t *testing.T) {
	b := WithProjects(BlockEnrichment{SchemaVersion: enrich.SchemaVersion},
		[]enrich.ProjectAttribution{
			{ID: "products:atlas_platform", Confidence: 0.62, Source: "embedding"},
			{ID: "features:billing", Confidence: 0.51, Source: "embedding"},
		}, enrich.ProjectsAttributed,
		&enrich.AttributionMeta{EncoderState: "warm", Verifier: "not_needed",
			ModelVersions: map[string]string{"decision": "per-group-margin-v1"}}, nil)
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	i, j := strings.Index(s, `"products:atlas_platform"`), strings.Index(s, `"features:billing"`)
	if i < 0 || j < 0 || i > j {
		t.Fatalf("both ids must ride in order under `projects`: %s", s)
	}
	if !strings.Contains(s, `"decision":"per-group-margin-v1"`) {
		t.Fatalf("model_versions must carry the decision marker: %s", s)
	}
}
