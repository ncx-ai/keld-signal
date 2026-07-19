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

	// Agentic-framework context (empty for coding tools). Set when the request
	// originates from an agent step (Mastra/LangChain/LangGraph/CrewAI) so the
	// classifier can reason over the framework/agent/workflow context.
	Framework   string   // mastra, langchain, langgraph, crewai
	AgentRole   string   // e.g. research_agent, billing_assistant
	Workflow    string   // workflow/graph name
	Step        string   // node/step id or index
	RecentSteps []string // prior agent steps, newest-first
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
	// Agentic fields append AFTER the coding fields, so coding-tool preambles are
	// byte-identical (all prior facet numbers unchanged).
	if m.Framework != "" {
		parts = append(parts, "framework: "+m.Framework)
	}
	if m.AgentRole != "" {
		parts = append(parts, "agent: "+m.AgentRole)
	}
	if m.Workflow != "" {
		parts = append(parts, "workflow: "+m.Workflow)
	}
	if m.Step != "" {
		parts = append(parts, "step: "+m.Step)
	}
	var b strings.Builder
	b.WriteString("[Context — " + strings.Join(parts, "; ") + "]\n")
	if len(m.RecentPrompts) > 0 {
		b.WriteString("Recent prompts (newest first):\n")
		for i, p := range m.RecentPrompts {
			fmt.Fprintf(&b, " %d. %s\n", i+1, p)
		}
	}
	if len(m.RecentSteps) > 0 {
		b.WriteString("Recent steps (newest first):\n")
		for i, s := range m.RecentSteps {
			fmt.Fprintf(&b, " %d. %s\n", i+1, s)
		}
	}
	b.WriteString("Task: ")
	return b.String()
}
