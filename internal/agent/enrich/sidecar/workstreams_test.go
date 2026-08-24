package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
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
	if _, present := got.Workstreams["branch"]; present {
		t.Error("an unattributed (null) dimension must be absent, never a Labeled with an empty Value")
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

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
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
	for _, forbidden := range []string{"Federico", "named_terms", "inventory", "453451c2",
		"window_start", "2026-08-23T00:00:00Z", "aurora-ledger", "beacon-api",
		"attributed", "sizer", "slice_start"} {
		if strings.Contains(string(b), forbidden) {
			t.Errorf("conversion leaked %q: %s", forbidden, b)
		}
	}
}

func TestAnalyzeLabeledReportsFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "missing", 60); ok {
		t.Errorf("a 404 must report failure, not an empty success, got %+v", got)
	}
}

// All dimensions unattributed is a real answer (ok), distinct from a failure.
func TestAnalyzeLabeledEmptyWindowIsASuccess(t *testing.T) {
	srv := analyzeServer(t, map[string]any{"schema": 1, "workstreams": map[string]any{"project": nil, "branch": nil}})
	defer srv.Close()
	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60)
	if !ok {
		t.Fatal("an all-unattributed window is a successful analysis, not a failure")
	}
	if len(got.Workstreams) != 0 {
		t.Errorf("want no dimensions, got %+v", got)
	}
}
