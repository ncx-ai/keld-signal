package publish

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

func sampleBlock() enrich.BlockCharacterisation {
	w := sampleWindow()
	novel := false
	departure := 0.12
	agrees := true
	// The session prior rides a block row exactly as it rides a window row: a
	// CONTRAST beside `workstreams`, never a value supplied in its place.
	w.Analysis.Prior = map[string]enrich.Prior{
		"branch": {Value: "main", Share: 0.78, Evidence: 140, Status: "attributed",
			Agrees: &agrees, Departure: &departure, Novel: &novel},
	}
	return enrich.BlockCharacterisation{
		SessionID: w.SessionID,
		Source:    w.Source,
		Ref: enrich.BlockRef{
			Start: "2026-08-19T12:50:00Z", End: "2026-08-19T13:10:00Z",
			SpanMinutes: 20, Evidence: 63,
			StartReason: "idle", EndReason: "budget",
		},
		StartTS:  1787144000,
		EndTS:    1787145200,
		Analysis: w.Analysis,
	}
}

// THE ATLAS CONTRACT. Every field the built endpoint reads must be on the row,
// spelled the way it reads it. Asserted against the marshalled bytes rather
// than the struct, because a missing json tag is exactly the defect that would
// pass a struct-level check and 422 in production.
func TestABlockRowMatchesTheAtlasContract(t *testing.T) {
	body, err := json.Marshal(BuildBlock(sampleBlock(), "dg@keld.co", time.Unix(1787145300, 0)))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{
		"source", "correlation", "session_id", "actor", "window",
		"start_reason", "end_reason",
		"workstreams", "dynamics", "prior", "effort",
		"pipeline_status", "extractor_versions", "schema_version", "ts",
	} {
		if _, ok := raw[k]; !ok {
			t.Errorf("a block row is missing %q — Atlas reads it", k)
		}
	}
	// The span is mandatory: Atlas 422s a block with no span, and half-open
	// [start, end) is the convention on both sides.
	win, _ := raw["window"].(map[string]any)
	if win["start"] != "2026-08-19T12:50:00Z" || win["end"] != "2026-08-19T13:10:00Z" {
		t.Errorf("window = %v", win)
	}
	// The session is mandatory too, and stated both ways Atlas accepts.
	if raw["session_id"] != "a8f58d56-f6e0-4f32-a78c-9d85e1d8df37" {
		t.Errorf("session_id = %v", raw["session_id"])
	}
	corr, _ := raw["correlation"].(map[string]any)
	if corr["session_id"] != raw["session_id"] {
		t.Errorf("correlation.session_id = %v, want the same session", corr["session_id"])
	}
	if raw["pipeline_status"] != enrich.PipelineStatusBlock {
		t.Errorf("pipeline_status = %v", raw["pipeline_status"])
	}
	// The two boundary reasons are the only fields distinguishing an arithmetic
	// cut from a real pause. They ride at the top level, the spelling Atlas
	// reads, as well as inside `window`.
	if raw["start_reason"] != "idle" || raw["end_reason"] != "budget" {
		t.Errorf("reasons = %v/%v", raw["start_reason"], raw["end_reason"])
	}
}

// The identity Atlas upserts on is (session, block.start), and the emitter
// depends on it: it advances its cursor only past confirmed publishes and
// recovers by RE-SENDING, which is only free if a re-sent block is the same
// row. Deterministic, per block, and never colliding with a prompt or a tick
// window.
func TestABlockCorrelationIsDeterministicAndPerBlock(t *testing.T) {
	b := sampleBlock()
	one := BuildBlock(b, "x", time.Now()).Correlation
	two := BuildBlock(b, "x", time.Now().Add(time.Hour)).Correlation
	if one.ID != two.ID {
		t.Fatalf("the same block produced two ids: %q vs %q", one.ID, two.ID)
	}
	if one.Scheme != enrich.BlockCorrScheme {
		t.Errorf("scheme = %q", one.Scheme)
	}
	if one.Scheme == enrich.WindowCorrScheme {
		t.Error("a block must not share the tick window's scheme — two different units " +
			"on one key are indistinguishable")
	}
	next := b
	next.Ref.Start = "2026-08-19T13:10:00Z"
	if BuildBlock(next, "x", time.Now()).Correlation.ID == one.ID {
		t.Fatal("two blocks of one session share an id — they would overwrite each other")
	}
	// Two spellings of one instant must not become two ids.
	same := b
	same.Ref.Start = "2026-08-19T13:50:00+01:00"
	if BuildBlock(same, "x", time.Now()).Correlation.ID != one.ID {
		t.Fatal("the same instant in two zones produced two ids")
	}
}

// A block row computes no text facet, so it must not be able to STATE one.
// Enrichment's facets are structs without omitempty: a zero value serialises as
// {"value":"","confidence":0}, which Atlas reads as a classification of the
// empty string. Its own struct is what makes that unrepresentable.
func TestABlockRowStatesNoTextFacetItNeverComputed(t *testing.T) {
	body, _ := json.Marshal(BuildBlock(sampleBlock(), "x", time.Now()))
	for _, k := range []string{"task_type", "domain", "sensitivity", "activity_type",
		"personal", "function_guess", "subcategory", "sensitivity_spans", "entities"} {
		if strings.Contains(string(body), `"`+k+`"`) {
			t.Errorf("a block row carries %q — no prompt text was read, so stating it "+
				"would report a classification nobody made", k)
		}
	}
}

// The same allowlist discipline the window row is held to: every new key is a
// channel a transcript fragment could occupy.
func TestTheBlockWireShapeCannotCarryAnalysisInternals(t *testing.T) {
	body, err := json.Marshal(BuildBlock(sampleBlock(), "x", time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"source": true, "correlation": true, "session_id": true, "actor": true,
		"window": true, "start_reason": true, "end_reason": true,
		"workstreams": true, "dynamics": true, "effort": true, "physical_acts": true,
		"files": true, "directories": true, "components": true, "inventory_omitted": true,
		"harness_tools": true, "programs": true, "external_systems": true, "integrations": true,
		"file_types": true, "shell_verbs": true, "subagents": true, "mcp_servers": true,
		// named_terms is allowed DELIBERATELY and is the only key here whose
		// values come from message text rather than tool-call inputs. See
		// AnalysisFacets.
		"named_terms":     true,
		"prior":           true,
		"pipeline_status": true, "extractor_versions": true, "schema_version": true, "ts": true,
	}
	for k := range raw {
		if !allowed[k] {
			t.Errorf("unexpected key %q on a block row — every new key is a channel a "+
				"transcript fragment could occupy; add it to `allowed` only after checking "+
				"it cannot", k)
		}
	}
}

// The shared facet struct must INLINE, not nest. Atlas reads the inventories
// off the raw body as top-level keys, so an accidental json tag on the embedded
// field would silently hide every one of them behind a `facets` object.
func TestTheSharedFacetsInlineOnBothRowTypes(t *testing.T) {
	blockBody, _ := json.Marshal(BuildBlock(sampleBlock(), "x", time.Now()))
	windowBody, _ := json.Marshal(BuildWindow(sampleWindow(), "x", time.Now()))
	for _, body := range []string{string(blockBody), string(windowBody)} {
		if strings.Contains(body, `"AnalysisFacets"`) || strings.Contains(body, `"facets"`) {
			t.Fatalf("the shared facets nested instead of inlining:\n%s", body)
		}
		// `prior` is deliberately not in this list: sampleWindow carries none, and
		// the point here is that what IS present inlines. The block row's prior
		// is asserted in the Atlas-contract test above.
		for _, k := range []string{`"workstreams"`, `"named_terms"`, `"inventory_omitted"`,
			`"effort"`, `"physical_acts"`} {
			if !strings.Contains(body, k) {
				t.Errorf("shared facet %s missing from a row:\n%s", k, body)
			}
		}
	}
}

func TestSendBlocksPostsABatchEnvelopeAndTheIngestToken(t *testing.T) {
	var gotBody []byte
	var gotToken, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotToken = r.Header.Get("x-keld-ingest-token")
		gotType = r.Header.Get("content-type")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	p := New(srv.URL, func() string { return "tok" }, "actor")
	rows := []BlockEnrichment{
		BuildBlock(sampleBlock(), "actor", time.Now()),
		BuildBlock(sampleBlock(), "actor", time.Now()),
	}
	if err := p.SendBlocks(rows); err != nil {
		t.Fatalf("SendBlocks: %v", err)
	}
	if gotToken != "tok" || gotType != "application/json" {
		t.Errorf("headers = %q/%q", gotToken, gotType)
	}
	var env struct {
		Blocks []map[string]any `json:"blocks"`
	}
	if err := json.Unmarshal(gotBody, &env); err != nil {
		t.Fatalf("body is not the {\"blocks\":[...]} envelope: %v", err)
	}
	if len(env.Blocks) != 2 {
		t.Fatalf("envelope carried %d blocks, want the whole batch", len(env.Blocks))
	}
}

func TestSendBlocksErrorsOnAnAtlasRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
	}))
	defer srv.Close()
	p := New(srv.URL, func() string { return "t" }, "a")
	if err := p.SendBlocks([]BlockEnrichment{BuildBlock(sampleBlock(), "a", time.Now())}); err == nil {
		t.Fatal("a 422 must surface as an error — the emitter holds its cursor on it")
	}
}

// An empty batch is not a request. Posting one would be a no-op round-trip on
// every quiet sweep.
func TestSendBlocksSkipsAnEmptyBatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("an empty batch reached the network")
	}))
	defer srv.Close()
	if err := New(srv.URL, func() string { return "t" }, "a").SendBlocks(nil); err != nil {
		t.Fatal(err)
	}
}

// ⚠️ `covers` IS DELETED FROM THE BLOCK ROW, and this asserts the SUBSTRING is
// absent rather than the key, for the reason every unforwardable-key test here
// does: a field revived under a different tag, or nested inside `window`, would
// pass a top-level key check and still put a prompt-id space back on the wire.
//
// The argument, so nobody restores it: a block is TIME end to end — a cap, a
// silence threshold, a rollup over a span — and Atlas has to join
// `event_ts ∈ [start, end)` within the session for COST anyway, because a turn
// spanning several blocks would double-count spend through `covers`. That join
// also answers the display question (Atlas holds ToolEvent.session_id and
// event_ts), so `covers` was a second, weaker copy of it. It also shipped
// broken: watch/filter.go yields `promptId` and the sidecar's store indexes the
// per-message `uuid`, so every real run published an empty list.
func TestABlockRowCarriesNoCoversMapping(t *testing.T) {
	body, err := json.Marshal(BuildBlock(sampleBlock(), "dg@keld.co", time.Unix(1787145300, 0)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "covers") {
		t.Fatalf("a block row still carries `covers` — a block is (principal, session, span, "+
			"reasons, facets) and nothing else:\n%s", body)
	}
	if strings.Contains(string(body), "prompt_id") {
		t.Fatalf("a block row names a prompt id:\n%s", body)
	}
}

// The type behind the row, not just this marshalling of it. A `Covers` field
// left on the struct with `json:"-"` would satisfy the wire test above and still
// force every producer to keep resolving prompt ids to fill it.
func TestTheBlockTypesHaveNoCoversFieldAtAll(t *testing.T) {
	for _, rt := range []reflect.Type{
		reflect.TypeOf(enrich.BlockCharacterisation{}),
		reflect.TypeOf(BlockEnrichment{}),
		reflect.TypeOf(enrich.BlockRef{}),
	} {
		for i := 0; i < rt.NumField(); i++ {
			name := rt.Field(i).Name
			if strings.Contains(strings.ToLower(name), "cover") ||
				strings.Contains(strings.ToLower(name), "prompt") {
				t.Errorf("%s.%s: the block path has no prompt-id space", rt.Name(), name)
			}
		}
	}
}
