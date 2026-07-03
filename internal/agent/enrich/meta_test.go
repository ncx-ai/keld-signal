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
