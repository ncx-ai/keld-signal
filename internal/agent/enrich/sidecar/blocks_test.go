package sidecar

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
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
		"workstreams": map[string]any{
			"branch": map[string]any{"value": "main", "share": 1.0, "status": "attributed", "evidence": 9},
		},
		"inventory": map[string]any{
			"physical_acts": []any{map[string]any{"value": "read", "n": 4}},
			"named_terms":   []any{map[string]any{"value": "Federico", "n": 2}},
		},
	}
}

// The request must carry coordinates and instants — and nothing else. Since
// `covers` was deleted it carries no ids either, so there is no longer any list
// on this call that a text lister could have filled by mistake.
func TestBlocksSendsCoordinatesAndInstantsOnly(t *testing.T) {
	var got map[string]any
	srv := blocksServer(t, map[string]any{"blocks": []any{}, "watermark": 99.0}, &got)
	defer srv.Close()

	since := 7.5
	_, ok := New(srv.URL, 5*time.Second).Blocks("/tmp/t.jsonl", &since,
		time.Unix(1000, 0), 24, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("Blocks reported failure on a 200")
	}
	want := map[string]bool{"path": true, "since_ts": true, "now": true, "max_blocks": true}
	for k := range got {
		if !want[k] {
			t.Errorf("unexpected key %q — /blocks takes coordinates and instants, never text "+
				"and never an id", k)
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

	New(srv.URL, 5*time.Second).Blocks("/tmp/t.jsonl", nil, time.Unix(1, 0), 0,
		enrich.ResolvedFacts{})
	if v, ok := got["since_ts"]; !ok || v != nil {
		t.Fatalf("since_ts = %v, want an explicit null", v)
	}
}

func TestBlocksCharacterisedConvertsThroughTheSameGatesAWindowRowUses(t *testing.T) {
	srv := blocksServer(t, map[string]any{
		"blocks":    []any{oneBlock(9, "idle", "budget")},
		"watermark": 3000.0,
	}, nil)
	defer srv.Close()

	ans := New(srv.URL, 5*time.Second).BlocksCharacterised("/tmp/t.jsonl", "claude_code",
		"sess-abc", nil, time.Unix(1, 0), 24, enrich.ResolvedFacts{})
	got := ans.Blocks
	if !ans.OK || len(got) != 1 {
		t.Fatalf("got %d blocks ok=%v", len(got), ans.OK)
	}
	if ans.Watermark == nil || *ans.Watermark != 3000.0 {
		t.Fatalf("watermark = %v, want 3000", ans.Watermark)
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

	ans := New(srv.URL, 5*time.Second).BlocksCharacterised("/t.jsonl", "claude_code",
		"s", nil, time.Unix(1, 0), 24, enrich.ResolvedFacts{})
	got := ans.Blocks
	if !ans.OK {
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

	ans := New(srv.URL, 5*time.Second).BlocksCharacterised("/t.jsonl", "claude_code",
		"s", nil, time.Unix(1, 0), 24, enrich.ResolvedFacts{})
	if !ans.OK || len(ans.Blocks) != 0 {
		t.Fatalf("got %d blocks ok=%v, want none", len(ans.Blocks), ans.OK)
	}
}

// A transcript the store has never ingested answers a NULL watermark, and that
// must reach the caller as nil rather than 0: the emitter seeds its
// forward-only cursor from this value, and 0 is a real instant in 1970.
func TestBlocksCharacterisedPassesANullWatermarkThrough(t *testing.T) {
	srv := blocksServer(t, map[string]any{"blocks": []any{}, "watermark": nil}, nil)
	defer srv.Close()

	ans := New(srv.URL, 5*time.Second).BlocksCharacterised("/t.jsonl", "claude_code",
		"s", nil, time.Unix(1, 0), 24, enrich.ResolvedFacts{})
	if !ans.OK {
		t.Fatal("not ok")
	}
	if ans.Watermark != nil {
		t.Fatalf("watermark = %v, want nil", *ans.Watermark)
	}
}

func TestBlocksReportsFailureRatherThanAnEmptyAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusForbidden)
	}))
	defer srv.Close()
	ans := New(srv.URL, 2*time.Second).BlocksCharacterised("/t.jsonl", "claude_code",
		"s", nil, time.Unix(1, 0), 24, enrich.ResolvedFacts{})
	if ans.OK {
		t.Fatal("a 403 must not read as a successful empty answer — the emitter would " +
			"retire the transcript and stop asking")
	}
	if ans.RouteUnsupported {
		t.Fatal("a 403 is a REFUSED path, not a missing route — reporting version skew " +
			"here would send the user to reinstall a sidecar that is perfectly current")
	}
}

// ⚠️ THE REQUEST CARRIES NO PROMPTS AT ALL. `covers` is deleted, so the id list
// that fed it has nowhere to go — and the honest pin is on the WIRE, because the
// sidecar's request model tolerates an unknown key silently (pydantic's
// extra="ignore", which is what lets the two halves ship separately). A Go side
// that kept sending them would look fine from either end.
func TestBlocksSendsNoPromptIDsBecauseThereIsNoCoversMapping(t *testing.T) {
	var got map[string]any
	srv := blocksServer(t, map[string]any{"blocks": []any{}, "watermark": 99.0}, &got)
	defer srv.Close()

	since := 7.5
	if _, ok := New(srv.URL, 5*time.Second).Blocks("/tmp/t.jsonl", &since,
		time.Unix(1000, 0), 24, enrich.ResolvedFacts{}); !ok {
		t.Fatal("Blocks reported failure on a 200")
	}
	if _, ok := got["prompts"]; ok {
		t.Errorf("the request still carries `prompts`: %v", got)
	}
	for k := range got {
		if strings.Contains(k, "prompt") {
			t.Errorf("unexpected prompt-shaped key %q on a /blocks request", k)
		}
	}
}

// A sidecar that still answers with `covers` is version skew, and the Go side
// must DROP it rather than model it. Pinned on the decoded value: an unknown key
// in the response is ignored by encoding/json, and this asserts that is the whole
// of what happens to it.
func TestABlocksResponseCoversKeyIsDroppedNotModelled(t *testing.T) {
	b := oneBlock(9, "idle", "budget")
	b["covers"] = []any{map[string]any{"prompt_id": "p1", "from": 1000.0, "to": 1500.0}}
	srv := blocksServer(t, map[string]any{"blocks": []any{b}, "watermark": 3000.0}, nil)
	defer srv.Close()

	ans := New(srv.URL, 5*time.Second).BlocksCharacterised("/tmp/t.jsonl", "claude_code",
		"sess-abc", nil, time.Unix(1, 0), 24, enrich.ResolvedFacts{})
	if !ans.OK || len(ans.Blocks) != 1 {
		t.Fatalf("got %d blocks ok=%v — a legacy `covers` key must not cost the block",
			len(ans.Blocks), ans.OK)
	}
	rt := reflect.TypeOf(BlockResult{})
	for i := 0; i < rt.NumField(); i++ {
		if strings.Contains(strings.ToLower(rt.Field(i).Name), "cover") {
			t.Errorf("BlockResult.%s: the response's covers key must not be modelled",
				rt.Field(i).Name)
		}
	}
}

// AC-8. A sidecar with no /blocks route at all answers 404, and that is version
// skew rather than "could not answer this time".
//
// ⚠️ THE DISTINCTION IS THE WHOLE FIX. Under a bare ok=false this arrived at the
// emitter identically to a store that is behind, whose correct response — hold
// the cursor, say nothing, retry next sweep — is exactly wrong here: no later
// sweep can succeed either. Measured cost of not distinguishing them: an Aug 11
// sidecar under a 2.3.0 daemon published ZERO blocks for ~3 weeks with telemetry
// flowing and `keld signal doctor` reporting no problems.
func TestBlocks404IsReportedAsAnUnsupportedRouteNotAnEmptyAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	ans := New(srv.URL, 2*time.Second).BlocksCharacterised("/t.jsonl", "claude_code",
		"s", nil, time.Unix(1, 0), 24, enrich.ResolvedFacts{})
	if ans.OK {
		t.Fatal("a 404 must not read as a successful answer")
	}
	if !ans.RouteUnsupported {
		t.Fatal("a 404 must report RouteUnsupported — otherwise a sidecar that predates " +
			"the route is indistinguishable from a quiet machine")
	}
	if len(ans.Blocks) != 0 || ans.Watermark != nil {
		t.Fatalf("a refused call must carry no rows and no watermark: %+v", ans)
	}
}

// A 503 is the one status the client retries through, and it must NEVER read as
// a missing route: the sidecar is present and merely busy, so sending the user
// to reinstall it would be wrong advice at the one moment the wait is working.
func TestBlocks503IsNotAnUnsupportedRoute(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	// NewCtx with a deadline, NOT New: 503 is the one status postStatus retries
	// through, deliberately and forever, so an unbounded client here hangs the
	// test rather than failing it. The ctx is the only thing that ends that loop
	// — which is itself the reason a 404 must not be routed into it.
	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	ans := NewCtx(ctx, srv.URL, 300*time.Millisecond).BlocksCharacterised("/t.jsonl", "claude_code",
		"s", nil, time.Unix(1, 0), 24, enrich.ResolvedFacts{})
	if ans.OK || ans.RouteUnsupported {
		t.Fatalf("503 must be neither success nor missing route: %+v", ans)
	}
}
