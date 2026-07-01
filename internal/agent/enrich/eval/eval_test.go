package eval

import "testing"

func TestScoreAccuracy(t *testing.T) {
	gold := []GoldRow{{TaskType: "codegen"}, {TaskType: "summarization"}}
	pred := []Pred{{TaskType: "codegen"}, {TaskType: "codegen"}}
	m := Score(gold, pred, []string{"task_type"})
	if m["task_type"]["accuracy"] != 0.5 {
		t.Fatalf("accuracy = %v, want 0.5", m["task_type"]["accuracy"])
	}
}

func TestScoreSensitiveRecall(t *testing.T) {
	gold := []GoldRow{{Sensitivity: "secrets"}, {Sensitivity: "none"}}
	pred := []Pred{{Sensitivity: "none"}, {Sensitivity: "none"}} // missed the secret
	m := Score(gold, pred, []string{"sensitivity"})
	if m["sensitivity"]["sensitive_recall"] != 0.0 {
		t.Fatalf("sensitive_recall = %v, want 0.0", m["sensitivity"]["sensitive_recall"])
	}
}

func TestScoreSensitiveRecallAllNoneIsOne(t *testing.T) {
	gold := []GoldRow{{Sensitivity: "none"}}
	pred := []Pred{{Sensitivity: "none"}}
	m := Score(gold, pred, []string{"sensitivity"})
	if m["sensitivity"]["sensitive_recall"] != 1.0 {
		t.Fatalf("sensitive_recall = %v, want 1.0", m["sensitivity"]["sensitive_recall"])
	}
}

func TestLoadGoldReadsEightRows(t *testing.T) {
	g, err := LoadGold()
	if err != nil {
		t.Fatal(err)
	}
	if len(g) != 8 {
		t.Fatalf("gold rows = %d, want 8", len(g))
	}
	if g[3].Sensitivity != "phi" || g[4].Sensitivity != "secrets" {
		t.Fatalf("unexpected gold sensitivity: %q %q", g[3].Sensitivity, g[4].Sensitivity)
	}
}
