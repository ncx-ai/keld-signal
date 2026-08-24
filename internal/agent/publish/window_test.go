package publish

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

func sampleWindow() enrich.WindowCharacterisation {
	b := true
	turnover := 0.4
	return enrich.WindowCharacterisation{
		SessionID: "a8f58d56-f6e0-4f32-a78c-9d85e1d8df37",
		Source:    "claude_code",
		Ref: enrich.WindowRef{
			Start: "2026-08-19T12:52:36Z", End: "2026-08-19T13:46:44Z",
			SpanMinutes: 54.133, Evidence: 63,
		},
		Analysis: enrich.WindowAnalysis{
			Workstreams:  map[string]enrich.Labeled{"branch": {Value: "main", Confidence: 0.9}},
			Dynamics:     map[string]enrich.Dynamic{"branch": {Status: "compared", Reading: "steady", Changed: &b, Turnover: &turnover}},
			PhysicalActs: []enrich.Act{{Value: "read", N: 12}},
		},
	}
}

// A tick row must not be able to overwrite a prompt's enrichment. Atlas keys
// enrichments UNIQUE(org_id, source_id, corr_scheme, corr_id) and inserts with
// ON CONFLICT DO UPDATE over every column, so a window row published under the
// anchor prompt's scheme+id would replace that prompt's task_type, sensitivity
// and domain with the nothing a tick computes. This is the assertion that keeps
// the design spec's refuted option (a) refuted.
func TestAWindowRowCannotCollideWithAPromptRow(t *testing.T) {
	w := sampleWindow()
	got := BuildWindow(w, "dev@example.com", time.Now())
	if got.Correlation.Scheme == "prompt_id" {
		t.Fatal("a window row under corr_scheme prompt_id would UPDATE that prompt's enrichment")
	}
	if got.Correlation.Scheme != enrich.WindowCorrScheme {
		t.Fatalf("scheme = %q, want %q", got.Correlation.Scheme, enrich.WindowCorrScheme)
	}
	// The id must also not BE a prompt id, or a future scheme-blind join would
	// still attach it to a turn it does not describe.
	if !strings.Contains(got.Correlation.ID, "@") {
		t.Fatalf("corr id %q is not window-shaped", got.Correlation.ID)
	}
}

// Idempotency: the same window republished is the same row, and two windows of
// one session are two rows. Both halves — an id that varied per call would
// duplicate under the unique key, and one that collapsed would overwrite.
func TestAWindowCorrelationIsDeterministicAndPerWindow(t *testing.T) {
	w := sampleWindow()
	a := BuildWindow(w, "x", time.Unix(1, 0)).Correlation.ID
	b := BuildWindow(w, "x", time.Unix(999999, 0)).Correlation.ID
	if a != b {
		t.Fatalf("corr id is not stable across publishes: %q vs %q", a, b)
	}
	next := w
	next.Ref.End = "2026-08-19T14:46:44Z"
	if c := BuildWindow(next, "x", time.Unix(1, 0)).Correlation.ID; c == a {
		t.Fatalf("two different windows share one corr id %q", c)
	}
	// Two spellings of one instant are one window, not two.
	same := w
	same.Ref.End = "2026-08-19T09:46:44-04:00"
	if d := BuildWindow(same, "x", time.Unix(1, 0)).Correlation.ID; d != a {
		t.Fatalf("one instant produced two corr ids: %q vs %q", d, a)
	}
}

// A tick reads no prompt text, so it computes no text facet. Those facets must
// be ABSENT, not present-and-empty: Enrichment declares them without omitempty
// and as structs, so reusing it would put `"task_type":{"value":"",...}` on the
// wire and Atlas would store an empty-string classification for every tick row.
func TestAWindowRowStatesNoTextFacetItNeverComputed(t *testing.T) {
	body, err := json.Marshal(BuildWindow(sampleWindow(), "x", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"task_type", "domain", "sensitivity", "activity_type",
		"personal", "function_guess", "subcategory", "entities", "sensitivity_spans",
		"prompt_chars"} {
		if _, present := raw[k]; present {
			t.Errorf("%q is on a window row; a facet nobody computed must be absent, "+
				"not published as an empty value", k)
		}
	}
	if raw["pipeline_status"] != enrich.PipelineStatusWindow {
		t.Errorf("pipeline_status = %v, want %q — a reader needs to be told WHY there is no "+
			"task_type here", raw["pipeline_status"], enrich.PipelineStatusWindow)
	}
}

// The bounds are what make the row interpretable at all, and the evidence count
// is what says it is not a characterisation of silence.
func TestAWindowRowCarriesItsBoundsAndItsBlocks(t *testing.T) {
	got := BuildWindow(sampleWindow(), "x", time.Now())
	if got.Window.Start == "" || got.Window.End == "" || got.Window.Evidence == 0 {
		t.Fatalf("window block is not self-describing: %+v", got.Window)
	}
	if got.Window.SpanMinutes != 54.133 {
		t.Errorf("span = %v, want the fractional gap width", got.Window.SpanMinutes)
	}
	if len(got.Workstreams) == 0 || len(got.Dynamics) == 0 || len(got.PhysicalActs) == 0 {
		t.Fatalf("the analysis blocks did not survive Build: %+v", got)
	}
	if got.SchemaVersion != enrich.SchemaVersion {
		t.Errorf("schema_version = %d, want %d", got.SchemaVersion, enrich.SchemaVersion)
	}
	if got.ExtractorVersions["workstreams"] == "" {
		t.Errorf("a window row is unattributed: %v", got.ExtractorVersions)
	}
}

// Same rule the prompt row's wire shape is held to: nothing a transcript wrote
// can reach Atlas through this struct. Asserted on the FIELD SET rather than on
// one payload, so adding a field that could carry one fails here.
func TestTheWindowWireShapeCannotCarryAnalysisInternals(t *testing.T) {
	body, err := json.Marshal(BuildWindow(sampleWindow(), "x", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"source": true, "correlation": true, "actor": true, "window": true,
		"workstreams": true, "dynamics": true, "effort": true, "physical_acts": true,
		"pipeline_status": true, "extractor_versions": true, "schema_version": true, "ts": true,
	}
	for k := range raw {
		if !allowed[k] {
			t.Errorf("unexpected key %q on a window row — every new key is a channel a "+
				"transcript fragment could occupy; add it to `allowed` only after checking "+
				"it cannot", k)
		}
	}
	// named_terms is the level that has held real person names. It is not
	// modelled anywhere on the path, so it cannot appear; assert it anyway,
	// because that is the one whose absence must never become accidental.
	if strings.Contains(string(body), "named_terms") {
		t.Fatal("named_terms reached the wire")
	}
}
