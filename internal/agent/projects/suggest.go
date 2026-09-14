package projects

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
)

// SuggestionKind is the closed vocabulary Suggestion.Kind publishes.
type SuggestionKind string

const (
	SuggestKindRepo      SuggestionKind = "repo"
	SuggestKindTicket    SuggestionKind = "ticket"
	SuggestKindWorkspace SuggestionKind = "workspace"
)

// Suggestion is one group of currently-unattributed blocks the health page
// can offer to bundle into a project. See docs/v3/contracts.md's
// `GET /v1/projects` response shape.
type Suggestion struct {
	ID      string         `json:"id"`
	Kind    SuggestionKind `json:"kind"`
	Value   string         `json:"value"`
	Blocks  int            `json:"blocks"`
	Minutes float64        `json:"minutes"`
	Tokens  int64          `json:"tokens"`
}

// UnattributedBlock is the minimal shape Suggest needs per block that
// Attribute already found no project for: its dims (never text) and its
// measured cost.
type UnattributedBlock struct {
	Dims    map[string]enrich.Labeled
	Minutes float64
	Tokens  int64
}

// SuggestionID is the stable id a (kind, value) pair always gets:
// sha1(kind+":"+value), hex-encoded, first 12 characters. Stable means a
// caller who bundles a repo, then removes it, sees the SAME suggestion id
// come back — nothing about the id depends on when it was computed or what
// else exists in the document.
func SuggestionID(kind SuggestionKind, value string) string {
	sum := sha1.Sum([]byte(string(kind) + ":" + value))
	return hex.EncodeToString(sum[:])[:12]
}

// groupKey is the same fallthrough order Attribute itself tries, restated for
// grouping rather than matching: repo, then ticket key (from the branch dim),
// then workspace (the `project` ALLOCATION dim — see DimWorkspace). A block
// with none of the three (e.g. a Cowork-shaped block with no repo and no
// ticket-bearing branch and no workspace dim) produces no group at all —
// never an empty or path-derived placeholder project.
func groupKey(dims map[string]enrich.Labeled) (SuggestionKind, string, bool) {
	if repo, ok := attributedValue(dims, DimRepo); ok {
		return SuggestKindRepo, normalizeRepo(repo), true
	}
	if branch, ok := attributedValue(dims, DimBranch); ok {
		if key, ok := ticketKeyIn(branch); ok {
			return SuggestKindTicket, key, true
		}
	}
	if ws, ok := attributedValue(dims, DimWorkspace); ok {
		return SuggestKindWorkspace, ws, true
	}
	return "", "", false
}

// Suggest groups unattributed blocks into suggestions: by repo, then ticket
// key, then workspace — the same order and the same three dimensions
// Attribute reads, restated in groupKey. Deterministic ordering (sorted by
// group key) so two calls over the same input produce byte-identical output.
func Suggest(blocks []UnattributedBlock) []Suggestion {
	type acc struct {
		kind    SuggestionKind
		value   string
		blocks  int
		minutes float64
		tokens  int64
	}
	groups := map[string]*acc{}
	var order []string
	for _, b := range blocks {
		kind, value, ok := groupKey(b.Dims)
		if !ok {
			continue
		}
		key := string(kind) + ":" + value
		g, exists := groups[key]
		if !exists {
			g = &acc{kind: kind, value: value}
			groups[key] = g
			order = append(order, key)
		}
		g.blocks++
		g.minutes += b.Minutes
		g.tokens += b.Tokens
	}
	sort.Strings(order)
	out := make([]Suggestion, 0, len(order))
	for _, key := range order {
		g := groups[key]
		out = append(out, Suggestion{
			ID:      SuggestionID(g.kind, g.value),
			Kind:    g.kind,
			Value:   g.value,
			Blocks:  g.blocks,
			Minutes: g.minutes,
			Tokens:  g.tokens,
		})
	}
	return out
}
