package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// analyzeServer serves one canned /analyze body.
func analyzeServer(t *testing.T, body map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(body)
	}))
}

func TestAnalyzeLabeledConvertsShareToConfidence(t *testing.T) {
	srv := analyzeServer(t, map[string]any{
		"schema": 1, "evidence": 9, "session": "453451c2",
		"window_start": "2026-08-23T00:00:00Z", "window_end": "2026-08-23T01:00:00Z",
		"workstreams": map[string]any{
			"project": map[string]any{"value": "keld-signal", "share": 0.812, "evidence": 43, "provenance": "known:tool_inputs"},
			"branch":  nil, // no dominant value: unattributed
		},
		"inventory": map[string]any{"named_terms": []map[string]any{{"value": "Federico", "n": 2}}},
	})
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	proj, present := got.Workstreams["project"]
	if !present {
		t.Fatalf("project dimension missing: %+v", got)
	}
	// share is a 0..1 dominance fraction — the natural Confidence.
	if proj.Value != "keld-signal" || proj.Confidence != 0.812 {
		t.Errorf("bad conversion: %+v", proj)
	}
	// A JSON null is what a sidecar OLDER than SCHEMA 16 sends for a dimension
	// it could not attribute; it carries no count and no status, so there is
	// nothing to publish and it is dropped. A SCHEMA 16 sidecar sends an object
	// saying `absent` instead — see
	// TestAnalyzeLabeledCarriesThinAndAbsentDimensionsWithTheirStatus.
	if _, present := got.Workstreams["branch"]; present {
		t.Error("a null dimension from a pre-16 sidecar must be absent, never a Labeled with an empty Value")
	}
	// The same fixture's `project` object carries NO status, which is the other
	// half of the pre-16 shape: that sidecar emitted an object only for a
	// dimension it had attributed, so that is how it is read. Anything else
	// would blank every workstream on a machine whose frozen sidecar is behind.
	if proj.Status != "attributed" {
		t.Errorf("a statusless pre-16 dimension must read as attributed, got %+v", proj)
	}
	if len(got.Workstreams) != 1 {
		t.Errorf("unexpected dimensions: %+v", got)
	}
}

// The privacy invariant, enforced structurally: /analyze's inventory (which
// carries named_terms lifted from message text, e.g. real person names) has
// nowhere to land, and neither do the window's session/timestamps, which are
// local metadata only.
func TestAnalyzeLabeledCarriesNoInventoryOrWindowMetadata(t *testing.T) {
	srv := analyzeServer(t, map[string]any{
		"schema": 1, "session": "453451c2",
		"window_start": "2026-08-23T00:00:00Z", "window_end": "2026-08-23T01:00:00Z",
		"workstreams": map[string]any{
			"project": map[string]any{"value": "keld-signal", "share": 1.0, "evidence": 3, "provenance": "known:tool_inputs"},
		},
		"inventory": map[string]any{
			"named_terms": []map[string]any{{"value": "Federico", "n": 2}},
			"programs":    []map[string]any{{"value": "git", "n": 9}},
		},
		// The dynamics block's per-side objects name the reference level itself
		// ("aurora-ledger" is a workspace, and on the `term` level the same slot
		// would hold a name spoken in conversation). None of it is modelled, so
		// none of it can reach the value this returns.
		"dynamics": map[string]any{
			"sizer": "ewma", "slice_start": "2026-08-23T00:45:00Z",
			"sizer_detail": map[string]any{"level": "branch"},
			"dimensions": map[string]any{
				"branch": map[string]any{
					"status": "compared", "turnover": 0.5, "decay": 0.1,
					"concentration_shift": nil, "changed": true, "reading": "switched",
					"slice":    map[string]any{"value": "beacon-api", "share": 1.0, "evidence": 9, "reason": "attributed"},
					"baseline": map[string]any{"value": "aurora-ledger", "share": 1.0, "evidence": 30, "reason": "attributed"},
				},
			},
		},
	})
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dynamics["branch"].Reading != "switched" {
		t.Fatalf("the dynamics half is empty; the leak assertions below would be "+
			"vacuous for it: %+v", got.Dynamics)
	}
	// "Federico" came OFF this list when named_terms began publishing: the one
	// inventory read from message text now decodes like the other eight. The
	// dynamics per-side values ("aurora-ledger", "beacon-api", "sizer",
	// "slice_start") and the block's own metadata stay forbidden — those are
	// still structurally unrepresentable, and that is what this test is now
	// guarding.
	if len(got.NamedTerms) == 0 {
		t.Fatal("named_terms is empty; its removal from the forbidden list below is vacuous")
	}
	for _, forbidden := range []string{"inventory", "453451c2",
		"window_start", "2026-08-23T00:00:00Z", "aurora-ledger", "beacon-api",
		"sizer", "slice_start"} {
		if strings.Contains(string(b), forbidden) {
			t.Errorf("conversion leaked %q: %s", forbidden, b)
		}
	}
	// "attributed" is SCOPED TO THE DYNAMICS SUBTREE rather than checked over
	// the whole payload, and the scoping is not a weakening. What must not leak
	// is the dynamics per-side `reason` — the field that sits beside a per-side
	// `value` naming a reference level, which on `term` can be a person's name.
	// The workstream dimensions' own `status` is the same word for a different,
	// intended field, so a whole-payload substring check can no longer tell the
	// two apart. Guarded from both ends: the workstream status below must BE
	// "attributed" (so the scoping is real and not an accident of an empty
	// payload), and the dynamics subtree must not contain it anywhere.
	if got.Workstreams["project"].Status != "attributed" {
		t.Fatalf("the workstream status is not what makes the scoping necessary; "+
			"the check below would be vacuous: %+v", got.Workstreams)
	}
	dyn, err := json.Marshal(got.Dynamics)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(dyn), "attributed") {
		t.Errorf("the dynamics per-side `reason` leaked: %s", dyn)
	}
}

func TestAnalyzeLabeledReportsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "missing", 60, enrich.ResolvedFacts{}); ok {
		t.Errorf("a 404 must report failure, not an empty success, got %+v", got)
	}
}

// All dimensions unattributed is a real answer (ok), distinct from a failure.
func TestAnalyzeLabeledEmptyWindowIsASuccess(t *testing.T) {
	srv := analyzeServer(t, map[string]any{"schema": 1, "workstreams": map[string]any{"project": nil, "branch": nil}})
	defer srv.Close()
	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("an all-unattributed window is a successful analysis, not a failure")
	}
	if len(got.Workstreams) != 0 {
		t.Errorf("want no dimensions, got %+v", got)
	}
}

// SCHEMA 16: every dimension answers with an object stating its own attribution
// outcome, and the sub-floor ones are CARRIED rather than deleted. This is the
// decode-boundary half of the change — the count and the label have to survive
// the conversion to Labeled or the wire test downstream has nothing to publish.
//
// ⚠️ The floor is not touched here and cannot be: this side only reads what the
// sidecar decided. `thin` stays `thin`.
func TestAnalyzeLabeledCarriesThinAndAbsentDimensionsWithTheirStatus(t *testing.T) {
	srv := analyzeServer(t, map[string]any{
		"schema": 16,
		"workstreams": map[string]any{
			"project": map[string]any{"value": "keld-signal", "share": 0.9, "evidence": 30,
				"status": "attributed", "provenance": "known:tool_inputs"},
			// Four observations — one short of the floor of five, which is the
			// single largest bucket of what used to be discarded (198 slots).
			"tooling": map[string]any{"value": "pytest", "share": 1.0, "evidence": 4,
				"status": "thin", "provenance": "known:tool_inputs"},
			// The level never fired. A real answer, and a different one.
			"skill": map[string]any{"value": nil, "share": 0.0, "evidence": 0,
				"status": "absent", "provenance": "known:tool_inputs"},
			"language": map[string]any{"value": "Go", "share": 0.33, "evidence": 12,
				"status": "no_majority", "provenance": "known:tool_inputs"},
		},
	})
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if len(got.Workstreams) != 4 {
		t.Fatalf("a sub-floor dimension was dropped: %+v", got.Workstreams)
	}
	for dim, want := range map[string]enrich.Labeled{
		"project":  {Value: "keld-signal", Confidence: 0.9, Evidence: 30, Status: "attributed"},
		"tooling":  {Value: "pytest", Confidence: 1.0, Evidence: 4, Status: "thin"},
		"skill":    {Status: "absent"},
		"language": {Value: "Go", Confidence: 0.33, Evidence: 12, Status: "no_majority"},
	} {
		if got.Workstreams[dim] != want {
			t.Errorf("%s: got %+v, want %+v", dim, got.Workstreams[dim], want)
		}
	}
}

// The VOCABULARY GATE, same rule the dynamics and prior blocks follow: a status
// this binary cannot read is version skew from a separately-shipped sidecar, and
// the dimension drops WHOLE rather than publishing a value with an unreadable
// outcome beside it. A value whose status cannot be read renders as a confident
// one, which is the exact misreading the status exists to prevent — so half a
// dimension is worse than none.
func TestAnalyzeLabeledDropsADimensionWithAnUnreadableStatus(t *testing.T) {
	srv := analyzeServer(t, map[string]any{
		"schema": 99,
		"workstreams": map[string]any{
			"project": map[string]any{"value": "keld-signal", "share": 1.0, "evidence": 30,
				"status": "attributed", "provenance": "known:tool_inputs"},
			"tooling": map[string]any{"value": "pytest", "share": 1.0, "evidence": 4,
				"status": "provisional", "provenance": "known:tool_inputs"},
		},
	})
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if _, present := got.Workstreams["tooling"]; present {
		t.Errorf("an unreadable status must drop the dimension: %+v", got.Workstreams)
	}
	// ... and only that one. One unreadable dimension must not cost the others,
	// the same per-entry rule convertActs follows.
	if len(got.Workstreams) != 1 || got.Workstreams["project"].Value != "keld-signal" {
		t.Errorf("the readable dimensions were collateral damage: %+v", got.Workstreams)
	}
}
