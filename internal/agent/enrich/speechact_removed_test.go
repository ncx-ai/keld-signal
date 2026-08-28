package enrich

import (
	"strings"
	"testing"
)

// speech_act was DROPPED at schema v9. Measured live over 2,015 inferences
// (docs/superpowers/specs/2026-08-24-facet-value-results.md): accuracy 0.695
// against a 0.713 majority baseline — worth LESS than always answering
// `command`. It predicted `statement` 22 times and was right zero times, at up
// to full confidence.
//
// These tests pin the removal at the two surfaces that could quietly bring it
// back: the pass registry, and the gate.

func TestNoBuiltinPassIsSpeechAct(t *testing.T) {
	for _, ex := range append(Wave1(nil), Wave2()...) {
		if ex.Name() == "speech_act" {
			t.Fatalf("speech_act is registered again as %T", ex)
		}
		if strings.Contains(ex.Version(), "speech_act") {
			t.Fatalf("%s carries a speech_act producer string %q", ex.Name(), ex.Version())
		}
	}
}

// fragmentEverything answers every classification task with the readable text
// the dropped `fragment` label used to be scored against. Nothing in the
// pipeline may act on it: with speech_act gone, the gate's ONLY content-free
// signal is the model-free approval lexicon (prefilterContentFree).
type fragmentEverything struct{}

func (fragmentEverything) Classify(_ string, tasks map[string][]string) map[string][]Ranked {
	out := map[string][]Ranked{}
	for task := range tasks {
		out[task] = []Ranked{{Label: "a short follow-up or acknowledgement", Confidence: 1.0}}
	}
	return out
}
func (fragmentEverything) Entities(string, map[string]string) []Entity { return nil }
func (fragmentEverything) Extract(string, map[string]string, map[string][]string) ExtractResult {
	return ExtractResult{}
}

func TestGateIsModelFree(t *testing.T) {
	t.Setenv("KELD_ENRICH_GATE_ENABLED", "1")
	// Substantive by the lexicon's reckoning (not an approval phrase), and the
	// model calls it a fragment at confidence 1.0. It must still be enriched:
	// the gate's model half is gone with the facet that fed it.
	p := Run("well, alright then I suppose", "claude_code", Meta{}, fragmentEverything{})
	if p.PipelineStatus == "gated" {
		t.Fatalf("status = %q: the gate must consult no model output", p.PipelineStatus)
	}
	if p.TaskType.Producer == "" {
		t.Errorf("task_type must have run; got %+v", p.TaskType)
	}
}
