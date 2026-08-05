package clientevents

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// Custom-pass reject reasons ride enrich.custom.rejected as a `reason` field, so
// they pass through the free-text word cap — which replaces ANY value over
// maxFieldWords wholesale with "<redacted>". That is what made every one of the
// 403 events in keld-atlas#62 diagnostically empty: the reasons were real
// strings, just too wordy to survive. Reasons must therefore be terse codes.
func TestCustomRejectReasonsSurviveRedaction(t *testing.T) {
	// One pass per reject path in enrich.BuildCustomExtractors.
	_, _, rejects := enrich.BuildCustomExtractors([]enrich.CustomPass{
		{Key: "task_type", Kind: "single_label"},           // built-in key
		{Key: "a", Kind: "single_label", ConditionOn: "x"}, // conditioned, no labels_by_cond
		{Key: "b", Kind: "single_label"},                   // no labels
		{Key: "c", Kind: "entity"},                         // no entity labels
		{Key: "d", Kind: "structure"},                      // unsupported kind
	})
	if len(rejects) != 5 {
		t.Fatalf("got %d rejects, want 5 (one per path): %+v", len(rejects), rejects)
	}
	for _, rj := range rejects {
		got := redactFields(map[string]any{"reason": rj.Reason})["reason"]
		if got != rj.Reason {
			t.Errorf("reason %q survived redaction as %q — a redacted reason is no diagnostic at all",
				rj.Reason, got)
		}
	}
}
