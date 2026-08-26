package sidecar

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

func blocksServer(t *testing.T, body map[string]any, seen *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/blocks" {
			t.Errorf("path = %q — v2 has its own entry point and must not reach /analyze", r.URL.Path)
		}
		if seen != nil {
			json.NewDecoder(r.Body).Decode(seen)
		}
		json.NewEncoder(w).Encode(body)
	}))
}

func oneBlock(evidence int, startReason, endReason string) map[string]any {
	return map[string]any{
		"session":       "sess-1",
		"start":         1000.0,
		"end":           2200.0,
		"start_reason":  startReason,
		"end_reason":    endReason,
		"block_minutes": 20.0,
		"evidence":      evidence,
		"covers": []any{
			map[string]any{"prompt_id": "p1", "from": 1000.0, "to": 1500.0, "complete": true},
			// Zero-width: covers no time, so it names no episode.
			map[string]any{"prompt_id": "p2", "from": 1500.0, "to": 1500.0, "complete": false},
			// No id at all: names no prompt.
			map[string]any{"prompt_id": "", "from": 1600.0, "to": 1900.0, "complete": true},
		},
		"workstreams": map[string]any{
			"branch": map[string]any{"value": "main", "share": 1.0, "status": "attributed", "evidence": 9},
		},
		"inventory": map[string]any{
			"physical_acts": []any{map[string]any{"value": "read", "n": 4}},
			"named_terms":   []any{map[string]any{"value": "Federico", "n": 2}},
		},
	}
}

// The request must carry coordinates, ids and instants — and nothing else. This
// is the call that would leak prompt text if the emitter ever reached for the
// text lister instead of the ids one.
func TestBlocksSendsCoordinatesIDsAndInstantsOnly(t *testing.T) {
	var got map[string]any
	srv := blocksServer(t, map[string]any{"blocks": []any{}, "watermark": 99.0}, &got)
	defer srv.Close()

	since := 7.5
	_, ok := New(srv.URL, 5*time.Second).Blocks("/tmp/t.jsonl", []string{"P1", "P2"}, &since,
		time.Unix(1000, 0), 24, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("Blocks reported failure on a 200")
	}
	want := map[string]bool{"path": true, "since_ts": true, "prompts": true, "now": true,
		"max_blocks": true}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected key %q — /blocks takes coordinates, ids and instants, never text", k)
		}
	}
	if got["path"] != "/tmp/t.jsonl" || got["since_ts"] != 7.5 || got["now"] != 1000.0 ||
		got["max_blocks"] != 24.0 {
		t.Errorf("request = %v", got)
	}
}

// A never-emitted transcript must send a NULL since_ts, not zero: zero is the
// Unix epoch, and the sidecar reads null as "from the beginning of the session"
// — the two are opposite instructions and a zero would ask for 1970 onward.
func TestBlocksSendsANullSinceForAFirstCall(t *testing.T) {
	var got map[string]any
	srv := blocksServer(t, map[string]any{"blocks": []any{}, "watermark": nil}, &got)
	defer srv.Close()

	New(srv.URL, 5*time.Second).Blocks("/tmp/t.jsonl", nil, nil, time.Unix(1, 0), 0,
		enrich.ResolvedFacts{})
	if v, ok := got["since_ts"]; !ok || v != nil {
		t.Fatalf("since_ts = %v, want an explicit null", v)
	}
	// An omitted prompt list and an empty one mean the same thing; say so.
	if ps, ok := got["prompts"].([]any); !ok || len(ps) != 0 {
		t.Fatalf("prompts = %v, want an explicit empty list", got["prompts"])
	}
}

func TestBlocksCharacterisedConvertsThroughTheSameGatesAWindowRowUses(t *testing.T) {
	srv := blocksServer(t, map[string]any{
		"blocks":    []any{oneBlock(9, "idle", "budget")},
		"watermark": 3000.0,
	}, nil)
	defer srv.Close()

	got, wm, ok := New(srv.URL, 5*time.Second).BlocksCharacterised("/tmp/t.jsonl", "claude_code",
		"sess-abc", []string{"p1"}, nil, time.Unix(1, 0), 24, enrich.ResolvedFacts{})
	if !ok || len(got) != 1 {
		t.Fatalf("got %d blocks ok=%v", len(got), ok)
	}
	if wm == nil || *wm != 3000.0 {
		t.Fatalf("watermark = %v, want 3000", wm)
	}
	b := got[0]
	// The session comes from the CALLER, not the response: the response's
	// `session` is the store's own path digest and joins to nothing.
	if b.SessionID != "sess-abc" {
		t.Errorf("session = %q, want the caller's", b.SessionID)
	}
	if b.Ref.StartReason != "idle" || b.Ref.EndReason != "budget" {
		t.Errorf("reasons = %q/%q", b.Ref.StartReason, b.Ref.EndReason)
	}
	if b.StartTS != 1000 || b.EndTS != 2200 {
		t.Errorf("epoch bounds = %v/%v — the cursor is in these units", b.StartTS, b.EndTS)
	}
	if b.Ref.Start != "1970-01-01T00:16:40Z" || b.Ref.End != "1970-01-01T00:36:40Z" {
		t.Errorf("iso bounds = %q/%q", b.Ref.Start, b.Ref.End)
	}
	if b.Ref.SpanMinutes != 20 || b.Ref.Evidence != 9 {
		t.Errorf("span/evidence = %v/%v", b.Ref.SpanMinutes, b.Ref.Evidence)
	}
	// The same convert* functions a window row goes through: the workstream
	// carries its status and evidence, and the inventories arrive converted.
	if w := b.Analysis.Workstreams["branch"]; w.Value != "main" || w.Status != "attributed" || w.Evidence != 9 {
		t.Errorf("branch = %+v", w)
	}
	if len(b.Analysis.PhysicalActs) != 1 || b.Analysis.PhysicalActs[0].Value != "read" {
		t.Errorf("acts = %v", b.Analysis.PhysicalActs)
	}
	if len(b.Analysis.NamedTerms) != 1 {
		t.Errorf("named_terms = %v", b.Analysis.NamedTerms)
	}
	// Covers: only the entry that names a prompt AND covers time survives.
	if len(b.Covers) != 1 || b.Covers[0].PromptID != "p1" || !b.Covers[0].Complete {
		t.Fatalf("covers = %+v", b.Covers)
	}
	if b.Covers[0].From != 1000 || b.Covers[0].To != 1500 {
		t.Errorf("cover bounds = %v/%v", b.Covers[0].From, b.Covers[0].To)
	}
}

// A boundary reason this binary cannot read is version skew from a
// separately-shipped sidecar, and the reason is the ONLY field distinguishing
// an arithmetic cut from a real pause. The block drops whole rather than
// publishing an edge nobody can place.
func TestBlocksCharacterisedDropsABlockWithAnUnreadableReason(t *testing.T) {
	srv := blocksServer(t, map[string]any{
		"blocks": []any{
			oneBlock(9, "idle", "some_future_reason"),
			oneBlock(9, "budget", "idle"),
		},
		"watermark": 3000.0,
	}, nil)
	defer srv.Close()

	got, _, ok := New(srv.URL, 5*time.Second).BlocksCharacterised("/t.jsonl", "claude_code",
		"s", nil, nil, time.Unix(1, 0), 24, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("not ok")
	}
	if len(got) != 1 || got[0].Ref.EndReason != "idle" {
		t.Fatalf("got %d blocks, want only the readable one: %+v", len(got), got)
	}
}

// A block with no evidence is not a characterisation of nothing, it is the
// absence of one. Publishing it turns a quiet machine into a stream of empty
// rows — defence in depth against a sidecar that stopped dropping them.
func TestBlocksCharacterisedDropsAnEmptyOrSpanlessBlock(t *testing.T) {
	spanless := oneBlock(9, "idle", "budget")
	spanless["end"] = spanless["start"]
	srv := blocksServer(t, map[string]any{
		"blocks":    []any{oneBlock(0, "idle", "budget"), spanless},
		"watermark": 3000.0,
	}, nil)
	defer srv.Close()

	got, _, ok := New(srv.URL, 5*time.Second).BlocksCharacterised("/t.jsonl", "claude_code",
		"s", nil, nil, time.Unix(1, 0), 24, enrich.ResolvedFacts{})
	if !ok || len(got) != 0 {
		t.Fatalf("got %d blocks ok=%v, want none", len(got), ok)
	}
}

// A transcript the store has never ingested answers a NULL watermark, and that
// must reach the caller as nil rather than 0: the emitter seeds its
// forward-only cursor from this value, and 0 is a real instant in 1970.
func TestBlocksCharacterisedPassesANullWatermarkThrough(t *testing.T) {
	srv := blocksServer(t, map[string]any{"blocks": []any{}, "watermark": nil}, nil)
	defer srv.Close()

	_, wm, ok := New(srv.URL, 5*time.Second).BlocksCharacterised("/t.jsonl", "claude_code",
		"s", nil, nil, time.Unix(1, 0), 24, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("not ok")
	}
	if wm != nil {
		t.Fatalf("watermark = %v, want nil", *wm)
	}
}

func TestBlocksReportsFailureRatherThanAnEmptyAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()
	if _, _, ok := New(srv.URL, 2*time.Second).BlocksCharacterised("/t.jsonl", "claude_code",
		"s", nil, nil, time.Unix(1, 0), 24, enrich.ResolvedFacts{}); ok {
		t.Fatal("a 403 must not read as a successful empty answer — the emitter would " +
			"retire the transcript and stop asking")
	}
}
