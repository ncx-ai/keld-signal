package daemon

import (
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
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

type customStub struct{}

func (customStub) Name() string                                       { return "s" }
func (customStub) Version() string                                    { return "s" }
func (customStub) Run(*enrich.JobContext) (map[string]any, error) { return nil, nil }
