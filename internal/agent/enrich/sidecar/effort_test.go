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
func TestNothingInTheEffortSubtreeCanCarryATranscriptString(t *testing.T) {
	srv := analyzeServer(t, effortBody(map[string]any{
		"authored_bytes": 6520, "authoring_turns": 3, "authored_status": "attributed",
		"fast_share": 0.542, "gaps": 41, "tempo": "steered", "tempo_status": "attributed",
		// Everything below is what a future sidecar must not be able to leak
		// through this block, whether by accident or by a well-meant addition.
		"edit_preview": "func settleRetry(ctx context.Context) error {",
		"old_string":   "SUPER_SECRET_OLD_PAYLOAD",
		"new_string":   "SUPER_SECRET_NEW_PAYLOAD",
		"content":      "SUPER_SECRET_WRITE_PAYLOAD",
		"files":        []string{"/Users/dg/keld/services/api/queue.go"},
		// And the four REFUTED signals, which are measured but must not publish.
		"token_weight": 128740.5, "request_tokens": 4211, "tokens": 8422,
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
	for _, forbidden := range []string{
		"SUPER_SECRET_OLD_PAYLOAD", "SUPER_SECRET_NEW_PAYLOAD", "SUPER_SECRET_WRITE_PAYLOAD",
		"settleRetry", "edit_preview", "old_string", "new_string", "queue.go", "files",
		"token_weight", "request_tokens", "out_bytes", "error_rate", "n_thrash",
		"128740", "993211",
	} {
		if strings.Contains(string(b), forbidden) {
			t.Errorf("the effort conversion leaked %q: %s", forbidden, b)
		}
	}
}
