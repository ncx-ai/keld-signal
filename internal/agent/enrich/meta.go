package enrich

import (
	"fmt"
	"strings"
)

// Meta is the non-prompt context a classification pass may reason over. Repo (cwd)
// and tool (source) are always known; branch/project/recent-prompts are added for
// interactive coding tools so fragment prompts classify in context. Team/category
// are resolved server-side in Atlas, never here.
type Meta struct {
	Repo          string
	Tool          string
	GitBranch     string
	Project       string
	RecentPrompts []string // prior user prompts, newest-first (bounded upstream)
}

// Preamble renders a compact context block prepended to the text handed to
// CLASSIFICATION passes (never to entity/sensitivity passes, which need raw
// offsets). Only non-empty fields render; empty repo renders "none" for a stable
// shape. The current prompt follows "Task: " last.
func (m Meta) Preamble() string {
	parts := []string{"repository: none"}
	if m.Repo != "" {
		parts[0] = "repository: " + m.Repo
	}
	if m.GitBranch != "" {
		parts = append(parts, "branch: "+m.GitBranch)
	}
	if m.Project != "" {
		parts = append(parts, "project: "+m.Project)
	}
	if m.Tool != "" {
		parts = append(parts, "tool: "+m.Tool)
	}
	var b strings.Builder
	b.WriteString("[Context — " + strings.Join(parts, "; ") + "]\n")
	if len(m.RecentPrompts) > 0 {
		b.WriteString("Recent prompts (newest first):\n")
		for i, p := range m.RecentPrompts {
			fmt.Fprintf(&b, " %d. %s\n", i+1, p)
		}
	}
	b.WriteString("Task: ")
	return b.String()
}
