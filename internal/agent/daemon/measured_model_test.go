package daemon

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/publish"
)

// The measured cell's model is the model that served most of the block's
// requests, whatever the evidence floor says. A model is read off each
// request, not inferred: a block of two requests that both went to Opus used
// Opus. Dropping the name below the floor left 130 real blocks on one machine
// with tokens, no model and a $0 estimate (measured 2026-09-28).

func blockWithModel(status, value string) publish.BlockEnrichment {
	var r publish.BlockEnrichment
	r.Tokens = &enrich.BlockTokens{Input: 1000, Output: 1000, CacheRead: 10000, CacheCreation: 1000}
	r.Dimensions = map[string]enrich.Labeled{"model": {Value: value, Status: status, Evidence: 2}}
	return r
}

func TestAThinModelIsStillTheBlocksModelAndIsPriced(t *testing.T) {
	m, ok := measuredOf(blockWithModel("thin", "claude-opus-5"))
	if !ok || m.Model != "claude-opus-5" {
		t.Fatalf("want claude-opus-5, got %q (ok=%v)", m.Model, ok)
	}
	if m.EstimateUSD <= 0 {
		t.Fatalf("a block with a known model must be priced, got %v", m.EstimateUSD)
	}
}

func TestEveryOutcomeThatNamesAModelRecordsIt(t *testing.T) {
	for _, status := range []string{"attributed", "thin", "tie", "no_majority", ""} {
		m, _ := measuredOf(blockWithModel(status, "claude-sonnet-5"))
		if m.Model != "claude-sonnet-5" {
			t.Errorf("status %q: want the leading model, got %q", status, m.Model)
		}
	}
}

func TestABlockWithNoModelObservedStaysUnnamed(t *testing.T) {
	m, ok := measuredOf(blockWithModel("absent", ""))
	if !ok {
		t.Fatal("tokens were reported, so the block is measured")
	}
	if m.Model != "" || m.EstimateUSD != 0 {
		t.Fatalf("no model was observed: want no name and no estimate, got %q / %v", m.Model, m.EstimateUSD)
	}
}
