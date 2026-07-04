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

func TestGoldRowMetaFromContext(t *testing.T) {
	r := GoldRow{
		Text:          "ok do it",
		RecentPrompts: []string{"add the compliance flag", "right-align the pills"},
		Repo:          "keld-atlas", Branch: "feat/pills", Project: "Keld Atlas",
	}
	m := r.Meta("claude_code")
	if m.Repo != "keld-atlas" || m.GitBranch != "feat/pills" || m.Project != "Keld Atlas" || m.Tool != "claude_code" {
		t.Fatalf("meta base: %+v", m)
	}
	if len(m.RecentPrompts) != 2 || m.RecentPrompts[0] != "add the compliance flag" {
		t.Fatalf("recent: %v", m.RecentPrompts)
	}
}

func TestLoadGoldParsesContextFields(t *testing.T) {
	rows, err := LoadGold()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range rows {
		if len(r.RecentPrompts) > 0 {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("expected at least one gold row with recent_prompts context")
	}
}

func TestLoadGoldReadsExpandedSet(t *testing.T) {
	g, err := LoadGold()
	if err != nil {
		t.Fatal(err)
	}
	if len(g) < 50 {
		t.Fatalf("gold rows = %d, want >= 50 (expanded set)", len(g))
	}
	if g[3].Sensitivity != "phi" || g[4].Sensitivity != "secrets" {
		t.Fatalf("unexpected gold sensitivity: %q %q", g[3].Sensitivity, g[4].Sensitivity)
	}
	// Every sensitivity class must be represented so sensitive_recall is meaningful.
	seen := map[string]bool{}
	for _, r := range g {
		seen[r.Sensitivity] = true
	}
	for _, want := range []string{"none", "pii", "secrets", "phi", "pci", "proprietary"} {
		if !seen[want] {
			t.Fatalf("gold set missing sensitivity class %q", want)
		}
	}
}
