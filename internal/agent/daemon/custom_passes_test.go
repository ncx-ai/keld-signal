package daemon

import (
	"context"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/enrichtest"
	"github.com/ncx-ai/keld-signal/internal/agent/queue"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
)

func TestPassesFromSchemaSkipsBuiltinsAndStructure(t *testing.T) {
	th := 0.4
	s := &settings.EnrichmentSchema{Passes: map[string]settings.RemotePass{
		"task_type": {Key: "task_type", Kind: "single_label", Title: "TT", Labels: []settings.RemoteLabel{{ID: "a", Text: "a"}}},
		"nsfw":      {Key: "nsfw", Kind: "single_label", Title: "NSFW", Labels: []settings.RemoteLabel{{ID: "safe", Text: "safe"}}},
		"art":       {Key: "art", Kind: "multi_label", Title: "Art", ClsThreshold: &th, Labels: []settings.RemoteLabel{{ID: "code", Text: "code"}}},
		"weird":     {Key: "weird", Kind: "structure", Title: "W"},
	}}
	passes := passesFromSchema(s)
	if len(passes) != 4 {
		t.Fatalf("passesFromSchema forwarded %d, want 4", len(passes))
	}
	w1, w2, rej := enrich.BuildCustomExtractors(passes)
	_ = w2
	if len(w1) != 2 { // nsfw + art
		t.Fatalf("w1=%d want 2 (nsfw, art)", len(w1))
	}
	// task_type collides with built-in; weird is structure => 2 rejects
	if len(rej) != 2 {
		t.Fatalf("rejects=%v want 2", rej)
	}
}

func TestPassesFromSchemaNilIsEmpty(t *testing.T) {
	if p := passesFromSchema(nil); len(p) != 0 {
		t.Fatalf("nil schema should give no passes, got %v", p)
	}
}

func TestCustomHolderSwapAndNilSafe(t *testing.T) {
	var nilHolder *customHolder
	if w1, w2 := nilHolder.load(); w1 != nil || w2 != nil {
		t.Fatalf("nil holder load should be empty")
	}
	h := newCustomHolder()
	if w1, _ := h.load(); w1 != nil {
		t.Fatalf("expected empty initial holder")
	}
	h.store([]enrich.Extractor{customStub{}}, nil)
	if w1, _ := h.load(); len(w1) != 1 {
		t.Fatalf("swap failed")
	}
}

// End-to-end daemon threading: a stored holder's custom passes reach the
// published enrichment via process's customOpts... (the wiring the unit tests
// above don't exercise).
func TestProcessThreadsCustomPassesIntoPublishedEnrichment(t *testing.T) {
	// This test exercises custom-pass threading through the full pipeline; disable
	// enrichment gating (default ON) so the fake's speech_act=fragment fallback on
	// "hello world" doesn't gate the semantic/custom passes out.
	t.Setenv("KELD_ENRICH_GATE_ENABLED", "false")
	holder := newCustomHolder()
	w1, w2, _ := enrich.BuildCustomExtractors([]enrich.CustomPass{
		{Key: "nsfw", Kind: "single_label", Title: "NSFW", Version: "1",
			Labels: []enrich.CustomLabel{{ID: "safe", Text: "safe"}, {ID: "nsfw", Text: "nsfw"}}},
	})
	holder.store(w1, w2)

	// Mirror Worker's per-job snapshot.
	cw1, cw2 := holder.load()
	copts := []enrich.Option{enrich.WithCustomExtractors(cw1, cw2)}

	sender := &fakeSender{}
	j := queue.Job{Source: "claude_code", Scheme: "prompt_id", ID: "CUS-1", Inline: "hello world"}
	ok := process(context.Background(), j, enrichtest.NewFake(), sender, "actor@keld.co",
		func() bool { return true }, nil, nil, nil, copts...)
	if !ok {
		t.Fatalf("process did not publish")
	}
	sent := sender.all()
	if len(sent) != 1 {
		t.Fatalf("want 1 publish, got %d", len(sent))
	}
	if _, present := sent[0].Custom["nsfw"]; !present {
		t.Fatalf("custom pass not threaded into published enrichment: %+v", sent[0].Custom)
	}
}

type customStub struct{}

func (customStub) Name() string                                       { return "s" }
func (customStub) Version() string                                    { return "s" }
func (customStub) Run(*enrich.JobContext) (map[string]any, error) { return nil, nil }
