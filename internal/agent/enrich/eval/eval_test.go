package eval

import (
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

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

type captureModel struct{ lastClassify string }

func (c *captureModel) Classify(text string, tasks map[string][]string) map[string][]enrich.Ranked {
	c.lastClassify = text
	out := map[string][]enrich.Ranked{}
	for name, labels := range tasks {
		if len(labels) > 0 {
			out[name] = []enrich.Ranked{{Label: labels[0], Confidence: 1}}
		}
	}
	return out
}
func (c *captureModel) Entities(string, map[string]string) []enrich.Entity { return nil }
func (c *captureModel) Extract(string, map[string]string, map[string][]string) enrich.ExtractResult {
	return enrich.ExtractResult{}
}

func TestRunModelWithContextFeedsRecentPrompts(t *testing.T) {
	gold := []GoldRow{{Text: "ok do it", RecentPrompts: []string{"add the compliance flag"}, Repo: "keld-atlas", Branch: "feat/x", Project: "Keld"}}
	aug := &captureModel{}
	_ = RunModelWithContext(aug, gold)
	if !strings.Contains(aug.lastClassify, "add the compliance flag") || !strings.Contains(aug.lastClassify, "Recent prompts") {
		t.Fatalf("augmented run must feed recent prompts into the classifier; got %q", aug.lastClassify)
	}
	base := &captureModel{}
	_ = RunModel(base, gold)
	if strings.Contains(base.lastClassify, "add the compliance flag") {
		t.Fatalf("baseline run must NOT include context; got %q", base.lastClassify)
	}
}

func TestLoadConfoundParsesClasses(t *testing.T) {
	rows, err := LoadConfound()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) < 10 {
		t.Fatalf("confound rows = %d, want >= 10", len(rows))
	}
	seen := map[string]int{}
	for _, r := range rows {
		seen[r.Class]++
	}
	for _, c := range []string{"c1", "c2", "c3"} {
		if seen[c] == 0 {
			t.Fatalf("confound set missing class %q", c)
		}
	}
	// c1 rows must be gold-labeled eng (the whole point).
	for _, r := range rows {
		if r.Class == "c1" && r.FunctionGuess != "eng" {
			t.Fatalf("c1 row not gold-eng: %q", r.Text)
		}
	}
}

func TestLeakageAndFalseEng(t *testing.T) {
	gold := []GoldRow{
		{Class: "c1", FunctionGuess: "eng", TaskType: "codegen"}, // leaked below
		{Class: "c1", FunctionGuess: "eng", TaskType: "codegen"}, // correct below
		{Class: "c2", FunctionGuess: "mkt"},                      // false-eng below
	}
	pred := []Pred{
		{FunctionGuess: "mkt", TaskType: "summarization"}, // c1 leaked (function+task)
		{FunctionGuess: "eng", TaskType: "codegen"},       // c1 correct
		{FunctionGuess: "eng"},                            // c2 → wrongly eng
	}
	lk := LeakageRate(gold, pred)
	if lk["function_guess"] != 0.5 {
		t.Fatalf("function leakage = %v, want 0.5", lk["function_guess"])
	}
	if lk["task_type"] != 0.5 {
		t.Fatalf("task leakage = %v, want 0.5", lk["task_type"])
	}
	if fe := FalseEngRate(gold, pred); fe != 1.0 {
		t.Fatalf("false_eng = %v, want 1.0", fe)
	}
}
