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

// HasAgentic reports whether any agentic-framework field is set.
func (m Meta) HasAgentic() bool {
	return m.Framework != "" || m.AgentRole != "" || m.Workflow != "" || m.Step != "" || len(m.RecentSteps) > 0
}

// Preamble renders the FULL context block (coding + agentic fields). Used by the
// domain classifier, where the agentic agent/workflow context is a measured help.
func (m Meta) Preamble() string { return m.build(true) }

// PreambleCoding renders ONLY the coding-tool context (repo/branch/project/tool +
// recent prompts), never the agentic fields. Used by task_type and the other
// non-domain classifiers, where agentic metadata is subject-noise that HURTS
// (measured: agentic task_type 0.83→0.62 with full metadata). For coding-tool
// requests (no agentic fields) this is byte-identical to Preamble().
func (m Meta) PreambleCoding() string { return m.build(false) }

// build renders a compact context block prepended to the text handed to
// CLASSIFICATION passes (never to entity/sensitivity passes, which need raw
// offsets). Only non-empty fields render; empty repo renders "none" for a stable
// shape. Agentic fields (and the recent-steps block) render only when agentic=true.
func (m Meta) build(agentic bool) string {
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
	if agentic {
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
	}
	var b strings.Builder
	b.WriteString("[Context — " + strings.Join(parts, "; ") + "]\n")
	if len(m.RecentPrompts) > 0 {
		b.WriteString("Recent prompts (newest first):\n")
		for i, p := range m.RecentPrompts {
			fmt.Fprintf(&b, " %d. %s\n", i+1, p)
		}
	}
	if agentic && len(m.RecentSteps) > 0 {
		b.WriteString("Recent steps (newest first):\n")
		for i, s := range m.RecentSteps {
			fmt.Fprintf(&b, " %d. %s\n", i+1, s)
		}
	}
	b.WriteString("Task: ")
	return b.String()
}
