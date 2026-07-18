package eval

import "testing"

func TestSpeechActScoredField(t *testing.T) {
	gold := []GoldRow{{Text: "a", SpeechAct: "question"}, {Text: "b", SpeechAct: "command"}}
	pred := []Pred{{SpeechAct: "question"}, {SpeechAct: "statement"}}
	m := Score(gold, pred, []string{"speech_act"})
	if got := m["speech_act"]["accuracy"]; got != 0.5 {
		t.Fatalf("speech_act accuracy = %.3f, want 0.5", got)
	}
}

func TestS1DownstreamBaseline(t *testing.T) {
	gold := []GoldRow{
		{Class: "s1", TaskType: "reasoning"},                    // trapped facet: task_type
		{Class: "s1", TaskType: "rag_qa", Activity: "retrieve"}, // two trapped facets
		{Class: "c1", TaskType: "codegen"},                      // not s1 → ignored
	}
	pred := []Pred{
		{TaskType: "codegen"},                    // wrong (1/1)
		{TaskType: "rag_qa", Activity: "generate"}, // task_type right, activity wrong (1/2)
		{TaskType: "codegen"},
	}
	// pairs: row0 task_type(wrong), row1 task_type(right)+activity(wrong) = 3 pairs, 2 wrong.
	if got := S1DownstreamBaseline(gold, pred); got != 2.0/3.0 {
		t.Fatalf("s1 downstream baseline = %.3f, want %.3f", got, 2.0/3.0)
	}
}

func TestSpeechActPerMood(t *testing.T) {
	gold := []GoldRow{{SpeechAct: "question"}, {SpeechAct: "question"}, {SpeechAct: "fragment"}}
	pred := []Pred{{SpeechAct: "question"}, {SpeechAct: "statement"}, {SpeechAct: "fragment"}}
	m := SpeechActPerMood(gold, pred)
	if m["question"] != [2]int{1, 2} {
		t.Fatalf("question = %v, want [1 2]", m["question"])
	}
	if m["fragment"] != [2]int{1, 1} {
		t.Fatalf("fragment = %v, want [1 1]", m["fragment"])
	}
}
