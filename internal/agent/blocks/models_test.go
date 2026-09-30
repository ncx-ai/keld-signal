package blocks

import (
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// ModelsFor re-asks the analysis service which model each of a session's
// earlier blocks used — for ledger rows recorded before 2026-09-29, when a
// block under the evidence floor was stored with no model at all.

func charAt(start int64, model string) enrich.BlockCharacterisation {
	dims := map[string]enrich.Labeled{}
	if model != "" {
		dims["model"] = enrich.Labeled{Value: model, Status: "thin", Evidence: 2}
	}
	return enrich.BlockCharacterisation{
		SessionID: "sess1",
		Ref:       enrich.BlockRef{Start: time.Unix(start, 0).UTC().Format(time.RFC3339), End: time.Unix(start+600, 0).UTC().Format(time.RFC3339)},
		StartTS:   float64(start),
		EndTS:     float64(start + 600),
		Analysis:  enrich.WindowAnalysis{Dimensions: dims},
	}
}

func TestModelsForNamesTheBlocksAsked(t *testing.T) {
	dig := &fakeDig{all: []enrich.BlockCharacterisation{
		charAt(1000, "claude-opus-5"), charAt(2000, "gpt-6-astra"), charAt(3000, "claude-opus-5-5"),
	}}
	e := newTestEmitter(t, dig, &fakeSender{})
	e.Advance("claude_code", "/t/sess1.jsonl")

	got, ok := e.ModelsFor("sess1", []int64{1000, 3000}, time.Now())
	if !ok {
		t.Fatal("the service answered, so the lookup must report ok")
	}
	if got[1000] != "claude-opus-5" || got[3000] != "claude-opus-5-5" || len(got) != 2 {
		t.Fatalf("want exactly the two asked blocks named, got %v", got)
	}
}

func TestModelsForLeavesABlockTheServiceSawNoModelInUnnamed(t *testing.T) {
	dig := &fakeDig{all: []enrich.BlockCharacterisation{charAt(1000, "")}}
	e := newTestEmitter(t, dig, &fakeSender{})
	e.Advance("codex", "/t/sess1.jsonl")
	got, ok := e.ModelsFor("sess1", []int64{1000}, time.Now())
	if !ok || len(got) != 0 {
		t.Fatalf("no model observed means no name, never a guess: got %v ok=%v", got, ok)
	}
}

func TestModelsForSaysNotYetWhenTheServiceCannotAnswer(t *testing.T) {
	e := newTestEmitter(t, &fakeDig{fail: true}, &fakeSender{})
	e.Advance("claude_code", "/t/sess1.jsonl")
	if _, ok := e.ModelsFor("sess1", []int64{1000}, time.Now()); ok {
		t.Fatal("a service that did not answer must not read as 'nothing to name'")
	}
}

func TestModelsForAnUnknownSessionIsAnsweredWithNothing(t *testing.T) {
	e := newTestEmitter(t, &fakeDig{}, &fakeSender{})
	got, ok := e.ModelsFor("never-seen", []int64{1000}, time.Now())
	if !ok || len(got) != 0 {
		t.Fatalf("no transcript known: nothing to ask, and nothing to retry; got %v ok=%v", got, ok)
	}
}
