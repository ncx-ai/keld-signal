package projects

import (
	"sort"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// Dimension keys evidence.go reads. These are NEVER inputs to Attribute (see
// attribute.go's evidence-is-not-a-rule test) — they describe a project after
// the fact, computed fresh from the blocks already attributed to it.
const (
	DimLanguage = "language"
	DimTooling  = "tooling"
)

// NameCount is one observed value and how many attributed blocks carried it.
type NameCount struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// Evidence is what a machine has observed about a project's blocks —
// branches, languages, tools, ticket keys and workspaces seen — computed ON
// READ, never stored as a rule and never matched on. It is purely
// descriptive: a person reads it to understand what a project's rules have
// actually been catching, never a signal Attribute consults.
type Evidence struct {
	Branches   []NameCount `json:"branches,omitempty"`
	Languages  []NameCount `json:"languages,omitempty"`
	Tools      []NameCount `json:"tools,omitempty"`
	TicketKeys []NameCount `json:"ticket_keys,omitempty"`
	Workspaces []NameCount `json:"workspaces,omitempty"`
}

// AttributedBlock is what EvidenceFor needs per block already resolved to a
// project: the project id Attribute returned, and the same dims map every
// caller already threads through Attribute/Suggest. Deliberately not a full
// block shape — this package has no dependency on the ledger store or the
// block emitter's own types.
type AttributedBlock struct {
	ProjectID string
	Dims      map[string]enrich.Labeled
}

// EvidenceFor summarises every block currently attributed to projectID.
// Branches also contribute to TicketKeys when the branch carries one, mirroring
// the same extraction Attribute's step 2 uses — so a project's evidence shows
// which ticket prefixes its branches have actually carried, independent of
// whether TicketKey happens to be set.
func EvidenceFor(projectID string, blocks []AttributedBlock) Evidence {
	branches := map[string]int{}
	languages := map[string]int{}
	tools := map[string]int{}
	tickets := map[string]int{}
	workspaces := map[string]int{}

	for _, b := range blocks {
		if b.ProjectID != projectID {
			continue
		}
		if v, ok := attributedValue(b.Dims, DimBranch); ok {
			branches[v]++
			if key, ok := ticketKeyIn(v); ok {
				tickets[key]++
			}
		}
		if v, ok := attributedValue(b.Dims, DimLanguage); ok {
			languages[v]++
		}
		if v, ok := attributedValue(b.Dims, DimTooling); ok {
			tools[v]++
		}
		if v, ok := attributedValue(b.Dims, DimWorkspace); ok {
			workspaces[v]++
		}
	}

	return Evidence{
		Branches:   sortedCounts(branches),
		Languages:  sortedCounts(languages),
		Tools:      sortedCounts(tools),
		TicketKeys: sortedCounts(tickets),
		Workspaces: sortedCounts(workspaces),
	}
}

// sortedCounts orders by count DESC, then name ASC for a stable tie-break, so
// two calls over the same map produce byte-identical output (Go map
// iteration order is randomised).
func sortedCounts(m map[string]int) []NameCount {
	if len(m) == 0 {
		return nil
	}
	out := make([]NameCount, 0, len(m))
	for name, count := range m {
		out = append(out, NameCount{Name: name, Count: count})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	return out
}
