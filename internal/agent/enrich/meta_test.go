package enrich

import "testing"

func TestPreamble(t *testing.T) {
	// baseline: repo + tool, no context extras
	got := Meta{Repo: "acme/api", Tool: "Claude Code"}.Preamble()
	want := "[Context — repository: acme/api; tool: Claude Code]\nTask: "
	if got != want {
		t.Fatalf("baseline preamble\n got: %q\nwant: %q", got, want)
	}

	// empty repo renders "none" for a stable shape
	if p := (Meta{}).Preamble(); p != "[Context — repository: none]\nTask: " {
		t.Fatalf("empty preamble: %q", p)
	}

	// branch + project appear in the context line; recent prompts as a numbered, newest-first block
	full := Meta{
		Repo: "acme/api", Tool: "Claude Code", GitBranch: "feat/pills", Project: "Keld Atlas",
		RecentPrompts: []string{"right-align the pills", "add a compliance flag"},
	}.Preamble()
	want = "[Context — repository: acme/api; branch: feat/pills; project: Keld Atlas; tool: Claude Code]\n" +
		"Recent prompts (newest first):\n 1. right-align the pills\n 2. add a compliance flag\nTask: "
	if full != want {
		t.Fatalf("full preamble\n got: %q\nwant: %q", full, want)
	}
}

func TestPreambleAgentic(t *testing.T) {
	// agentic context: framework/agent/workflow/step appended after coding fields
	// (repository:none base preserved), recent_steps as a newest-first block.
	got := Meta{
		Framework: "langgraph", AgentRole: "billing_assistant",
		Workflow: "research_pipeline", Step: "4",
		RecentSteps: []string{"search_web", "fetch_docs"},
	}.Preamble()
	want := "[Context — repository: none; framework: langgraph; agent: billing_assistant; workflow: research_pipeline; step: 4]\n" +
		"Recent steps (newest first):\n 1. search_web\n 2. fetch_docs\nTask: "
	if got != want {
		t.Fatalf("agentic preamble\n got: %q\nwant: %q", got, want)
	}
}

func TestPreambleCodingDropsAgentic(t *testing.T) {
	m := Meta{
		Repo: "acme/api", Tool: "Claude Code",
		Framework: "langgraph", AgentRole: "billing_assistant", Workflow: "wf", Step: "4",
		RecentSteps: []string{"search_web"},
	}
	// PreambleCoding keeps coding fields, drops ALL agentic fields + recent steps.
	got := m.PreambleCoding()
	want := "[Context — repository: acme/api; tool: Claude Code]\nTask: "
	if got != want {
		t.Fatalf("PreambleCoding must drop agentic\n got: %q\nwant: %q", got, want)
	}
	if !m.HasAgentic() {
		t.Fatal("HasAgentic should be true")
	}
	// For a pure coding Meta, PreambleCoding == Preamble (byte-identical).
	c := Meta{Repo: "acme/api", Tool: "Claude Code", GitBranch: "b", RecentPrompts: []string{"p"}}
	if c.PreambleCoding() != c.Preamble() {
		t.Fatalf("coding: PreambleCoding %q != Preamble %q", c.PreambleCoding(), c.Preamble())
	}
	if c.HasAgentic() {
		t.Fatal("HasAgentic should be false for coding Meta")
	}
}
