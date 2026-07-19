package eval

import "testing"

func TestLoadAgenticParses(t *testing.T) {
	rows, err := LoadAgentic()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("agentic corpus empty")
	}
}

func TestGoldRowMetaCarriesAgentic(t *testing.T) {
	r := GoldRow{Framework: "mastra", AgentRole: "billing", Workflow: "wf", Step: "2", RecentSteps: []string{"a"}}
	m := r.Meta("mastra")
	if m.Framework != "mastra" || m.AgentRole != "billing" || m.Workflow != "wf" || m.Step != "2" || len(m.RecentSteps) != 1 {
		t.Fatalf("agentic Meta not populated: %+v", m)
	}
}

func TestAccuracyByShape(t *testing.T) {
	gold := []GoldRow{
		{Shape: "clean", TaskType: "summarization"},
		{Shape: "clean", TaskType: "reasoning"},
		{Shape: "raw", TaskType: "summarization"},
	}
	pred := []Pred{
		{TaskType: "summarization"},   // clean hit
		{TaskType: "other"},           // clean miss
		{TaskType: "code_generation"}, // raw miss
	}
	m := AccuracyByShape(gold, pred, "task_type")
	if m["clean"] != [2]int{1, 2} {
		t.Fatalf("clean = %v, want [1 2]", m["clean"])
	}
	if m["raw"] != [2]int{0, 1} {
		t.Fatalf("raw = %v, want [0 1]", m["raw"])
	}
}
