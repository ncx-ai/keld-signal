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
