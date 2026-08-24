package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAnalyzeSendsCoordinatesAndNeverText(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/analyze" {
			t.Errorf("path = %q", r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		json.NewEncoder(w).Encode(map[string]any{
			"schema": 1, "evidence": 42, "session": "453451c2",
			"window_start": "2026-08-23T00:00:00Z", "window_end": "2026-08-23T01:00:00Z",
			"workstreams": map[string]any{
				"project": map[string]any{"value": "acme", "share": 1.0, "evidence": 3, "provenance": "known:tool_inputs"},
				"tooling": nil,
			},
			// A privacy-sensitive field the real sidecar also returns. The client
			// must not have anywhere to put this — see AnalyzeResult's doc comment.
			"inventory": map[string]any{
				"named_terms": []map[string]any{{"value": "Federico", "n": 2}},
			},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second)
	out, ok := c.Analyze("/tmp/t.jsonl", "prompt-1", 60)
	if !ok {
		t.Fatal("Analyze reported failure")
	}
	if _, leaked := got["text"]; leaked {
		t.Error("request must carry coordinates, never text")
	}
	if got["path"] != "/tmp/t.jsonl" || got["prompt_id"] != "prompt-1" {
		t.Errorf("coordinates not sent: %v", got)
	}
	if got["span_minutes"] != float64(60) {
		t.Errorf("span_minutes not sent: %v", got)
	}
	if out.Evidence != 42 || out.Schema != 1 || out.Session != "453451c2" {
		t.Errorf("bad decode: %+v", out)
	}
	proj := out.Workstreams["project"]
	if proj == nil || proj.Value != "acme" || proj.Share != 1.0 || proj.Evidence != 3 || proj.Provenance != "known:tool_inputs" {
		t.Errorf("bad workstream decode: %+v", proj)
	}
	if tooling := out.Workstreams["tooling"]; tooling != nil {
		t.Errorf("null workstream should decode to nil, got %+v", tooling)
	}
}

func TestAnalyzeReportsFailureOn404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if out, ok := New(srv.URL, 5*time.Second).Analyze("/tmp/t.jsonl", "missing", 60); ok {
		t.Errorf("a 404 must report failure, not an empty success, got %+v", out)
	}
}

// TestAnalyzeIgnoresTheDynamicsBlock pins the property the sidecar's dynamics
// block depends on: /analyze's response grew a "dynamics" object (the recent
// slice read against a longer baseline — sidecar/app/analysis/dynamics.py) and
// this client must decode the digest unchanged and forward none of it.
//
// Two halves, and both matter. Tolerance is not assumed from "encoding/json
// ignores unknown fields" — a DisallowUnknownFields anywhere in post() would
// turn every /analyze call into ok=false, i.e. a silently failed workstreams
// facet on every prompt, so it is asserted against a real payload. And
// AnalyzeResult deliberately has no field for the block, for the same reason it
// has none for "inventory": the values inside it are reference levels that stay
// on this machine, and structurally having nowhere to put them is stronger than
// a comment saying not to.
func TestAnalyzeIgnoresTheDynamicsBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"schema": 2, "evidence": 180, "session": "d19a4c72",
			"window_start": "2026-08-20T09:03:17Z", "window_end": "2026-08-20T10:03:17Z",
			"workstreams": map[string]any{
				"project": map[string]any{"value": "beacon-api", "share": 0.94, "evidence": 62, "provenance": "known:tool_inputs"},
			},
			"inventory": map[string]any{"named_terms": []map[string]any{}},
			"dynamics": map[string]any{
				"sizer": "fixed", "slice_minutes": 15.0, "baseline_minutes": 45.0,
				"source": "bin+event", "reconcile_scope": "file",
				"slice_start": "2026-08-20T09:48:17Z", "slice_end": "2026-08-20T10:03:17Z",
				"baseline_start": "2026-08-20T09:03:17Z", "sizer_detail": map[string]any{},
				"dimensions": map[string]any{
					"project": map[string]any{
						"status": "compared", "turnover": 1.0, "decay": 1.0,
						"concentration_shift": 0.4, "changed": true,
						"slice":    map[string]any{"value": "beacon-api", "share": 1.0, "evidence": 62, "reason": "attributed"},
						"baseline": map[string]any{"value": "aurora-ledger", "share": 1.0, "evidence": 118, "reason": "attributed"},
						"emerged":  map[string]any{"n": 1, "top": []map[string]any{{"value": "beacon-api", "share": 1.0}}},
						"decayed":  map[string]any{"n": 1, "top": []map[string]any{{"value": "aurora-ledger", "share": 1.0}}},
					},
					"tooling": map[string]any{
						"status": "both_absent", "turnover": nil, "decay": nil,
						"concentration_shift": nil, "changed": false,
					},
				},
			},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, 5*time.Second)
	out, ok := c.Analyze("/tmp/t.jsonl", "prompt-1", 60)
	if !ok {
		t.Fatal("a response carrying the dynamics block failed to decode")
	}
	if out.Evidence != 180 || out.Schema != 2 {
		t.Errorf("the digest did not survive the added block: %+v", out)
	}
	if proj := out.Workstreams["project"]; proj == nil || proj.Value != "beacon-api" {
		t.Errorf("bad workstream decode: %+v", proj)
	}
	// Nowhere to put it: the block must not be reachable through the labeled view
	// the enrichment pipeline actually consumes.
	labeled, ok := c.AnalyzeLabeled("/tmp/t.jsonl", "prompt-1", 60)
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if len(labeled) != 1 || labeled["project"].Value != "beacon-api" {
		t.Errorf("dynamics leaked into the labeled view: %+v", labeled)
	}
}
