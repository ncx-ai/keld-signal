package eval

import "testing"

// speech_act was dropped at schema v9 (accuracy 0.695 vs a 0.713 majority
// baseline — worth less than a constant). The two tests that scored the facet
// through Score/SpeechActPerMood went with it; nothing predicts it, so there is
// nothing to score.
//
// What stays is this file's OTHER subject: the s1 adversarial gold class, which
// traps task_type/activity_type on utterances whose mood misleads. Those facets
// ship, so the trap still measures something.

// TestGoldSpeechActLabelsAreRetained pins the deliberate asymmetry: the gold
// LABELS survive the facet's removal. They cost nothing, and they are the
// evidence any re-introduced classifier would have to be judged against — the
// study named the label WORDING (SpeechActDefs) as the suspect rather than the
// idea, so a re-bakeoff is a live option and a re-bakeoff needs ground truth.
//
// Deleting them would make that decision unmeasurable, which is the one
// irreversible way to remove a facet.
func TestGoldSpeechActLabelsAreRetained(t *testing.T) {
	gold, err := LoadGold()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range gold {
		if r.SpeechAct != "" {
			n++
		}
	}
	if n == 0 {
		t.Fatal("gold.jsonl speech_act labels were deleted: they are kept on purpose as the evidence for re-introducing a better classifier")
	}
	// Nothing scores them: with no producer, fieldOf must not resolve the facet
	// for a Pred, or Score would report a number for a facet nothing predicts.
	if got := fieldOf(Pred{}, "speech_act"); got != "" {
		t.Fatalf("fieldOf resolves speech_act for a Pred (%q): the facet has no producer", got)
	}
	m := Score(gold, make([]Pred, len(gold)), []string{"speech_act"})
	if _, ok := m["speech_act"]["accuracy"]; ok {
		t.Fatalf("speech_act reported an accuracy: %v", m["speech_act"])
	}
	t.Logf("retained %d gold speech_act labels, unscored", n)
}

func TestS1DownstreamBaseline(t *testing.T) {
	gold := []GoldRow{
		{Class: "s1", TaskType: "reasoning"},                                // trapped facet: task_type
		{Class: "s1", TaskType: "question_answering", Activity: "retrieve"}, // two trapped facets
		{Class: "c1", TaskType: "code_generation"},                          // not s1 -> ignored
	}
	pred := []Pred{
		{TaskType: "code_generation"},                          // wrong (1/1)
		{TaskType: "question_answering", Activity: "generate"}, // task_type right, activity wrong (1/2)
		{TaskType: "code_generation"},
	}
	// pairs: row0 task_type(wrong), row1 task_type(right)+activity(wrong) = 3 pairs, 2 wrong.
	if got := S1DownstreamBaseline(gold, pred); got != 2.0/3.0 {
		t.Fatalf("s1 downstream baseline = %.3f, want %.3f", got, 2.0/3.0)
	}
}
