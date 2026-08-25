package sidecar

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// effortBody is a realistic /analyze body carrying the effort block plus the
// neighbours that must not follow it across.
func effortBody(effort map[string]any) map[string]any {
	return map[string]any{
		"schema": 6, "evidence": 411, "session": "453451c2",
		"window_start": "2026-08-24T09:03:17Z", "window_end": "2026-08-24T10:03:17Z",
		"workstreams": map[string]any{
			"project": map[string]any{"value": "keld-signal", "share": 0.9, "evidence": 40,
				"provenance": "known:tool_inputs"},
		},
		"inventory": map[string]any{"named_terms": []map[string]any{{"value": "Federico", "n": 2}}},
		"effort":    effort,
	}
}

func TestAnalyzeLabeledCarriesTheEffortBlock(t *testing.T) {
	srv := analyzeServer(t, effortBody(map[string]any{
		"authored_bytes": 6520, "authoring_turns": 3, "authored_status": "attributed",
		"fast_share": 0.542, "gaps": 41, "tempo": "steered", "tempo_status": "attributed",
	}))
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if got.Effort == nil {
		t.Fatalf("effort block dropped: %+v", got)
	}
	e := got.Effort
	if e.AuthoredBytes == nil || *e.AuthoredBytes != 6520 || e.AuthoringTurns != 3 {
		t.Errorf("diff magnitude mangled: %+v", e)
	}
	if e.FastShare == nil || *e.FastShare != 0.542 || e.Gaps != 41 {
		t.Errorf("tempo share mangled: %+v", e)
	}
	if e.Tempo != "steered" || e.TempoStatus != "attributed" || e.AuthoredStatus != "attributed" {
		t.Errorf("vocabulary values mangled: %+v", e)
	}
}

// The block's spend and gap distribution: request_tokens is the window-scoped,
// price-weighted spend (NOT the raw per-event token counts Atlas already gets
// from telemetry — see enrich.Effort.RequestTokens), and gap_p50_s/gap_p90_s are
// the inter-turn gap distribution Task 1 computes. All three round-trip to
// enrich.Effort, and an absent one decodes to nil, never 0 — the same pointer
// discipline fast_share already holds to.
func TestEffortCarriesTheSpendAndGapDistribution(t *testing.T) {
	srv := analyzeServer(t, effortBody(map[string]any{
		"authored_bytes": 6520, "authoring_turns": 3, "authored_status": "attributed",
		"fast_share": 0.542, "gaps": 41, "tempo": "steered", "tempo_status": "attributed",
		"request_tokens": 18422, "gap_p50_s": 12.5, "gap_p90_s": 187.0,
	}))
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if got.Effort == nil {
		t.Fatalf("effort block dropped: %+v", got)
	}
	e := got.Effort
	if e.RequestTokens == nil || *e.RequestTokens != 18422 {
		t.Errorf("request_tokens mangled: %+v", e)
	}
	if e.GapP50S == nil || *e.GapP50S != 12.5 {
		t.Errorf("gap_p50_s mangled: %+v", e)
	}
	if e.GapP90S == nil || *e.GapP90S != 187.0 {
		t.Errorf("gap_p90_s mangled: %+v", e)
	}
}

// The three new fields are each independently absent-able: a sidecar that
// cannot compute one (no priced turns, or fewer than latency.MIN_GAPS gaps)
// must send null for that one alone, and it must decode to nil, never 0 — 0
// spend and 0-second gaps are both measured, readable answers.
func TestEffortSpendAndGapsDecodeToNoValueNotZero(t *testing.T) {
	srv := analyzeServer(t, effortBody(map[string]any{
		"authored_bytes": nil, "authoring_turns": 0, "authored_status": "absent",
		"fast_share": nil, "gaps": 0, "tempo": nil, "tempo_status": "absent",
		"request_tokens": nil, "gap_p50_s": nil, "gap_p90_s": nil,
	}))
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if got.Effort == nil {
		t.Fatalf("an abstaining block is still a block: %+v", got)
	}
	if got.Effort.RequestTokens != nil {
		t.Errorf("request_tokens null became %v", *got.Effort.RequestTokens)
	}
	if got.Effort.GapP50S != nil {
		t.Errorf("gap_p50_s null became %v", *got.Effort.GapP50S)
	}
	if got.Effort.GapP90S != nil {
		t.Errorf("gap_p90_s null became %v", *got.Effort.GapP90S)
	}
}

// The one-turn window, across the wire. `null` must arrive as nil, never as 0 —
// which is the whole reason the field is a pointer.
func TestNullFastShareDecodesToNoValueNotZero(t *testing.T) {
	srv := analyzeServer(t, effortBody(map[string]any{
		"authored_bytes": nil, "authoring_turns": 0, "authored_status": "absent",
		"fast_share": nil, "gaps": 0, "tempo": nil, "tempo_status": "absent",
	}))
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if got.Effort == nil {
		t.Fatalf("an abstaining block is still a block: %+v", got)
	}
	if got.Effort.FastShare != nil {
		t.Errorf("fast_share null became %v; a one-turn window must not read as fully slow",
			*got.Effort.FastShare)
	}
	if got.Effort.AuthoredBytes != nil {
		t.Errorf("authored_bytes null became %v", *got.Effort.AuthoredBytes)
	}
	if got.Effort.TempoStatus != "absent" || got.Effort.AuthoredStatus != "absent" {
		t.Errorf("the statuses that make the nulls readable were lost: %+v", got.Effort)
	}
}

// The VOCABULARY GATE, same rule as convertDynamics: the sidecar is frozen and
// shipped separately from keld-agent, so an older or newer one can sit in
// ~/.local/bin indefinitely. A value this binary does not recognise is version
// skew, and forwarding it would publish a label no Atlas consumer's vocabulary
// contains. The whole block is dropped, not half of it — a share with an
// unreadable status is not interpretable.
func TestAnEffortBlockWithAnUnknownVocabularyValueIsDropped(t *testing.T) {
	for _, c := range []struct {
		name  string
		block map[string]any
	}{
		{"an unknown tempo", map[string]any{
			"authored_bytes": 10, "authoring_turns": 1, "authored_status": "attributed",
			"fast_share": 0.9, "gaps": 9, "tempo": "interactive", "tempo_status": "attributed"}},
		{"an unknown tempo status", map[string]any{
			"authored_bytes": 10, "authoring_turns": 1, "authored_status": "attributed",
			"fast_share": 0.9, "gaps": 9, "tempo": "steered", "tempo_status": "no_majority"}},
		{"an unknown authored status", map[string]any{
			"authored_bytes": 10, "authoring_turns": 1, "authored_status": "thin",
			"fast_share": 0.9, "gaps": 9, "tempo": "steered", "tempo_status": "attributed"}},
		{"a missing tempo status", map[string]any{
			"authored_bytes": 10, "authoring_turns": 1, "authored_status": "attributed",
			"fast_share": 0.9, "gaps": 9, "tempo": "steered"}},
		{"a missing authored status", map[string]any{
			"fast_share": 0.9, "gaps": 9, "tempo": "steered", "tempo_status": "attributed"}},
	} {
		srv := analyzeServer(t, effortBody(c.block))
		got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60, enrich.ResolvedFacts{})
		srv.Close()
		if !ok {
			t.Fatalf("%s: an unreadable block must not fail the analysis", c.name)
		}
		if got.Effort != nil {
			t.Errorf("%s: forwarded anyway: %+v", c.name, got.Effort)
		}
		// The rest of the response still publishes: the gate drops a block, not a call.
		if got.Workstreams["project"].Value != "keld-signal" {
			t.Errorf("%s: the digest half was collateral damage: %+v", c.name, got.Workstreams)
		}
	}
}

// A response with no effort block at all (an older sidecar) is a success with no
// block, never a zeroed one.
func TestNoEffortBlockIsAbsentNotZeroed(t *testing.T) {
	srv := analyzeServer(t, map[string]any{
		"schema": 5, "workstreams": map[string]any{
			"project": map[string]any{"value": "keld-signal", "share": 1.0, "evidence": 9},
		},
	})
	defer srv.Close()
	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	if got.Effort != nil {
		t.Errorf("a missing block became %+v", got.Effort)
	}
}

// The privacy invariant, structurally: the effort block is numbers and closed
// vocabularies, so a string the sidecar adds to it has nowhere to land. A byte of
// old_string/new_string/content is file contents; nothing in this subtree may be
// able to hold one, and that is enforced by the struct having no such field
// rather than by anyone remembering not to add one.
//
// `request_tokens` is deliberately ABSENT from the forbidden list below: it was
// one of three spellings tested for the REFUTED "token weight" candidate
// (alongside `token_weight`/`tokens`), but Task 1/2 revived the same wire name
// for a different, legitimate computation — the window-scoped, price-weighted
// spend (see enrich.Effort.RequestTokens) — so it now belongs on the wire and is
// asserted PRESENT instead, just below.
func TestNothingInTheEffortSubtreeCanCarryATranscriptString(t *testing.T) {
	srv := analyzeServer(t, effortBody(map[string]any{
		"authored_bytes": 6520, "authoring_turns": 3, "authored_status": "attributed",
		"fast_share": 0.542, "gaps": 41, "tempo": "steered", "tempo_status": "attributed",
		"request_tokens": 4211, "gap_p50_s": 12.5, "gap_p90_s": 187.0,
		// Everything below is what a future sidecar must not be able to leak
		// through this block, whether by accident or by a well-meant addition.
		"edit_preview": "func settleRetry(ctx context.Context) error {",
		"old_string":   "SUPER_SECRET_OLD_PAYLOAD",
		"new_string":   "SUPER_SECRET_NEW_PAYLOAD",
		"content":      "SUPER_SECRET_WRITE_PAYLOAD",
		"files":        []string{"/Users/dg/keld/services/api/queue.go"},
		// And the three still-REFUTED signals, which are measured but must not
		// publish.
		"token_weight": 128740.5, "tokens": 8422,
		"out_bytes": 993211, "error_rate": 0.11, "n_thrash": 4,
	}))
	defer srv.Close()

	got, ok := New(srv.URL, 5*time.Second).AnalyzeLabeled("/tmp/t.jsonl", "p1", 60, enrich.ResolvedFacts{})
	if !ok {
		t.Fatal("AnalyzeLabeled reported failure")
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	// Guard the guard: the block really did survive, or every check below is vacuous.
	if got.Effort == nil || got.Effort.Tempo != "steered" {
		t.Fatalf("the effort half is empty; the leak assertions would prove nothing: %+v", got)
	}
	// request_tokens is now legitimate and must survive.
	if got.Effort.RequestTokens == nil || *got.Effort.RequestTokens != 4211 {
		t.Errorf("request_tokens, now a real field, was dropped: %+v", got.Effort)
	}
	for _, forbidden := range []string{
		"SUPER_SECRET_OLD_PAYLOAD", "SUPER_SECRET_NEW_PAYLOAD", "SUPER_SECRET_WRITE_PAYLOAD",
		"settleRetry", "edit_preview", "old_string", "new_string", "queue.go", "files",
		"token_weight", "out_bytes", "error_rate", "n_thrash",
		"128740", "993211",
	} {
		if strings.Contains(string(b), forbidden) {
			t.Errorf("the effort conversion leaked %q: %s", forbidden, b)
		}
	}
}
