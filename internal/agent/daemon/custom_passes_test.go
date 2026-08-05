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
	// task_type is built-in and dropped here, so it never reaches the collision
	// guard; nsfw/art/weird forward and are sorted out downstream by kind.
	if len(passes) != 3 {
		t.Fatalf("passesFromSchema forwarded %d, want 3", len(passes))
	}
	w1, w2, rej := enrich.BuildCustomExtractors(passes)
	_ = w2
	if len(w1) != 2 { // nsfw + art
		t.Fatalf("w1=%d want 2 (nsfw, art)", len(w1))
	}
	// weird is structure => the only reject
	if len(rej) != 1 || rej[0].Key != "weird" {
		t.Fatalf("rejects=%v want 1 (weird)", rej)
	}
}

// A healthy Atlas response lists the 8 built-ins alongside the org's custom
// passes, flagged is_system. Forwarding them made BuildCustomExtractors reject
// all 8 on EVERY poll — 403 warn events in the dogfood org (keld-atlas#62).
func TestPassesFromSchemaDropsSystemPasses(t *testing.T) {
	s := &settings.EnrichmentSchema{Passes: map[string]settings.RemotePass{
		"task_type": {Key: "task_type", Kind: "single_label", IsSystem: true,
			Labels: []settings.RemoteLabel{{ID: "a", Text: "a"}}},
		"nsfw": {Key: "nsfw", Kind: "single_label",
			Labels: []settings.RemoteLabel{{ID: "safe", Text: "safe"}}},
	}}
	passes := passesFromSchema(s)
	if len(passes) != 1 || passes[0].Key != "nsfw" {
		t.Fatalf("forwarded %+v, want only the custom pass nsfw", passes)
	}
	if _, _, rej := enrich.BuildCustomExtractors(passes); len(rej) != 0 {
		t.Fatalf("healthy schema produced rejects %+v, want none", rej)
	}
}

// An Atlas predating the is_system flag still lists built-ins in `passes`. Key
// is enough to recognize them, and must be, or the client only stops spamming
// once every deployment has upgraded.
func TestPassesFromSchemaDropsBuiltinKeyWithoutFlag(t *testing.T) {
	s := &settings.EnrichmentSchema{Passes: map[string]settings.RemotePass{
		"sensitivity": {Key: "sensitivity", Kind: "single_label",
			Labels: []settings.RemoteLabel{{ID: "pii", Text: "pii"}}},
	}}
	if passes := passesFromSchema(s); len(passes) != 0 {
		t.Fatalf("forwarded %+v, want none (sensitivity is built-in)", passes)
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

func (customStub) Name() string                                   { return "s" }
func (customStub) Version() string                                { return "s" }
func (customStub) Run(*enrich.JobContext) (map[string]any, error) { return nil, nil }
