package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func tickServer(t *testing.T, body map[string]any, seen *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/tick" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if seen != nil {
			json.NewDecoder(r.Body).Decode(seen)
		}
		json.NewEncoder(w).Encode(body)
	}))
}

func oneWindow(evidence int) map[string]any {
	return map[string]any{
		"window_start": "2026-08-19T12:52:36Z",
		"window_end":   "2026-08-19T13:46:44Z",
		"evidence":     evidence,
		"workstreams":  map[string]any{"branch": map[string]any{"value": "main", "share": 1.0}},
		"inventory":    map[string]any{"physical_acts": []any{map[string]any{"value": "read", "n": 4}}},
	}
}

func TestTickSendsCoordinatesAndInstantsOnly(t *testing.T) {
	var got map[string]any
	srv := tickServer(t, map[string]any{"cursor": 42.0, "windows": []any{}}, &got)
	defer srv.Close()

	cur := 7.5
	_, ok := New(srv.URL, 5*time.Second).Tick("/tmp/t.jsonl", []string{"P1", "P2"}, &cur,
		time.Unix(1000, 0), 60, 12)
	if !ok {
		t.Fatal("Tick reported failure on a 200")
	}
	want := map[string]bool{"path": true, "prompt_ids": true, "cursor_ts": true, "now": true,
		"span_minutes": true, "max_windows": true}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected key %q — a tick sends coordinates and instants, never text", k)
		}
	}
	if got["path"] != "/tmp/t.jsonl" || got["cursor_ts"] != 7.5 || got["now"] != 1000.0 {
		t.Errorf("request = %v", got)
	}
}

// A never-ticked transcript must send a NULL cursor, not zero: zero is the Unix
// epoch, which would make the planner try to characterise 1970 onward.
func TestANeverTickedTranscriptSendsANullCursor(t *testing.T) {
	var got map[string]any
	srv := tickServer(t, map[string]any{"cursor": 1.0, "windows": []any{}}, &got)
	defer srv.Close()
	New(srv.URL, 5*time.Second).Tick("/tmp/t.jsonl", nil, nil, time.Unix(1000, 0), 60, 12)
	if v, present := got["cursor_ts"]; !present || v != nil {
		t.Fatalf("cursor_ts = %v, want null", v)
	}
}

func TestTickCharacterisedConvertsAWindowTheSameWayAPromptsIsConverted(t *testing.T) {
	srv := tickServer(t, map[string]any{"cursor": 99.0, "windows": []any{oneWindow(63)}}, nil)
	defer srv.Close()

	wins, cursor, ok := New(srv.URL, 5*time.Second).TickCharacterised(
		"/tmp/t.jsonl", "claude_code", "sess-1", []string{"P1"}, nil, time.Unix(1000, 0), 60, 12)
	if !ok || cursor != 99 || len(wins) != 1 {
		t.Fatalf("ok=%v cursor=%v wins=%d", ok, cursor, len(wins))
	}
	w := wins[0]
	if w.SessionID != "sess-1" || w.Source != "claude_code" {
		t.Errorf("identity not carried: %+v", w)
	}
	if got := w.Analysis.Workstreams["branch"]; got.Value != "main" || got.Confidence != 1.0 {
		t.Errorf("share did not become confidence: %+v", got)
	}
	if len(w.Analysis.PhysicalActs) != 1 || w.Analysis.PhysicalActs[0].N != 4 {
		t.Errorf("acts did not survive: %+v", w.Analysis.PhysicalActs)
	}
	if w.Ref.Evidence != 63 {
		t.Errorf("evidence = %d", w.Ref.Evidence)
	}
	// The span is derived, fractional, and must be the real gap width: rounding
	// it up would describe a window reaching into the region the next prompt's
	// own look-back already covers.
	if got := w.Ref.SpanMinutes; got < 54.1 || got > 54.2 {
		t.Errorf("span = %v, want ~54.13 (the real gap width, not a rounded hour)", got)
	}
}

// "Idle ticks emit nothing", enforced on THIS side too. The sidecar already
// drops a window with no evidence, and this is the second gate: a
// characterisation of nothing is not a characterisation, and a quiet machine
// publishing one every interval forever is the failure the rule exists to stop.
// Belt and braces on purpose — the two halves ship as separate binaries and an
// older sidecar can sit in ~/.local/bin indefinitely.
func TestAWindowWithNoEvidenceIsNeverForwardedForPublication(t *testing.T) {
	srv := tickServer(t, map[string]any{
		"cursor":  5.0,
		"windows": []any{oneWindow(0), oneWindow(7)},
	}, nil)
	defer srv.Close()

	wins, _, ok := New(srv.URL, 5*time.Second).TickCharacterised(
		"/tmp/t.jsonl", "claude_code", "sess-1", []string{"P1"}, nil, time.Unix(1000, 0), 60, 12)
	if !ok {
		t.Fatal("not ok")
	}
	if len(wins) != 1 || wins[0].Ref.Evidence != 7 {
		t.Fatalf("an empty window reached the publisher: %+v", wins)
	}
}

// A window with no bounds cannot be located by anything downstream, so it is not
// a row — it is a row-shaped hole. Same gate, same reason.
func TestAWindowWithNoBoundsIsNeverForwarded(t *testing.T) {
	unbounded := oneWindow(9)
	unbounded["window_end"] = ""
	srv := tickServer(t, map[string]any{"cursor": 5.0, "windows": []any{unbounded}}, nil)
	defer srv.Close()
	wins, _, _ := New(srv.URL, 5*time.Second).TickCharacterised(
		"/tmp/t.jsonl", "claude_code", "sess-1", []string{"P1"}, nil, time.Unix(1000, 0), 60, 12)
	if len(wins) != 0 {
		t.Fatalf("an unlocatable window reached the publisher: %+v", wins)
	}
}

// A failed tick is not an empty tick. Advancing a cursor over windows that were
// never received would silently lose exactly the characterisation this path
// exists to add, so ok=false must carry no cursor a caller could commit.
func TestAFailedTickReportsNoCursor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	wins, cursor, ok := New(srv.URL, 5*time.Second).TickCharacterised(
		"/tmp/t.jsonl", "claude_code", "sess-1", []string{"P1"}, nil, time.Unix(1000, 0), 60, 12)
	if ok || cursor != 0 || wins != nil {
		t.Fatalf("a failed tick reported ok=%v cursor=%v wins=%v", ok, cursor, wins)
	}
}
