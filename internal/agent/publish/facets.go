package publish

import "github.com/ncx-ai/keld-signal/internal/agent/enrich"

// AnalysisFacets is the deterministic window analysis in its WIRE shape: the
// workstream dimensions, the dynamics, the effort block, all thirteen
// inventories, the cut-visibility map beside them, and the session prior.
//
// IT EXISTS SO TWO ROW TYPES CANNOT DRIFT, AND THAT IS ITS WHOLE JOB. A
// tick-emitted window (WindowEnrichment) and a v2 block (BlockEnrichment) carry
// exactly this set, because it is exactly one enrich.WindowAnalysis rendered
// for the wire — the same analysis over different bounds. Copying twenty
// `omitempty` fields into a second struct is how a fourteenth inventory comes
// to publish on one row and be silently missing from the other, and how a
// privacy gate written for one is forgotten for the other. So the fields live
// HERE, once, and both rows EMBED this rather than restating it. The design
// spec's rule for the sidecar — "if it is needed by both, it moves DOWN into a
// module both import, it is not called sideways" — applied on the Go side.
//
// It is embedded ANONYMOUSLY and carries no json tag of its own, so encoding/json
// inlines every field: both rows' wire shapes are byte-for-byte what they were
// when the fields were written out longhand. There is no `facets` object on the
// wire and there must never be one — Atlas reads these as top-level keys.
//
// ⚠️ EVERY FIELD HERE IS ALREADY GATED, and this struct adds no gate of its own.
// The vocabulary and structural checks are at the DECODE boundary
// (sidecar.analysisFrom and the convert* functions it composes), which is where
// a value from a separately-shipped, possibly-skewed sidecar first becomes
// something this binary is willing to name. Do not add a second, weaker copy of
// those checks here, and do not add a field that has not been through them.
type AnalysisFacets struct {
	Workstreams map[string]enrich.Labeled `json:"workstreams,omitempty"`
	Dynamics    map[string]enrich.Dynamic `json:"dynamics,omitempty"`
	Effort      *enrich.Effort            `json:"effort,omitempty"`
	// PhysicalActs is absent, never an empty list, when the span recorded no
	// act — same rule as the prompt row's.
	PhysicalActs []enrich.Act `json:"physical_acts,omitempty"`
	// Files, Directories and Components are what the span physically TOUCHED —
	// same rule and same meaning as the prompt row's (see Enrichment.Files).
	Files       []enrich.PathCount `json:"files,omitempty"`
	Directories []enrich.PathCount `json:"directories,omitempty"`
	Components  []enrich.PathCount `json:"components,omitempty"`
	// HarnessTools, Programs, ExternalSystems and Integrations are what the
	// span USED — same rule and same meaning as the prompt row's (see
	// Enrichment.HarnessTools).
	HarnessTools    []enrich.NameCount `json:"harness_tools,omitempty"`
	Programs        []enrich.NameCount `json:"programs,omitempty"`
	ExternalSystems []enrich.NameCount `json:"external_systems,omitempty"`
	Integrations    []enrich.NameCount `json:"integrations,omitempty"`
	// NamedTerms is the ninth inventory and the ONLY one drawn from message
	// TEXT rather than tool-call inputs: proper nouns lifted from the prompt,
	// matched against no declared vocabulary, observed to contain real person
	// names. It was withheld from the wire until that was reversed as an
	// explicit decision; it is bounded by shape only (see
	// sidecar.convertNamedTerms) and carries no person-name filter, because at
	// spaCy's measured ~1% precision a filter would create false assurance
	// rather than remove names.
	NamedTerms []enrich.NameCount `json:"named_terms,omitempty"`
	// FileTypes, ShellVerbs, Subagents and McpServers are the last four
	// inventories, over the `ext`, `verb`, `agent` and `mcp_server` levels.
	// Same analysis, same no-inference path, same per-entry structural gates.
	// Each COMPLEMENTS a sibling rather than restating it: what KIND of work
	// the Files were, the command where Programs is only the binary, the one
	// dimension that says work was DELEGATED, and the SERVER where Integrations
	// is the tool. Absent when the span used nothing in that dimension; never
	// an empty list.
	FileTypes  []enrich.NameCount `json:"file_types,omitempty"`
	ShellVerbs []enrich.NameCount `json:"shell_verbs,omitempty"`
	Subagents  []enrich.NameCount `json:"subagents,omitempty"`
	McpServers []enrich.NameCount `json:"mcp_servers,omitempty"`
	// InventoryOmitted is the cut-visibility map beside the inventories above —
	// same rule as the prompt row's (see Enrichment.InventoryOmitted).
	InventoryOmitted map[string]int `json:"inventory_omitted,omitempty"`
	// Prior is the SESSION this span sat in — same rule and same meaning as the
	// prompt row's (see Enrichment.Prior): a contrast reported beside
	// `workstreams`, never a value supplied in its place.
	Prior map[string]enrich.Prior `json:"prior,omitempty"`
}

// facetsOf renders one enrich.WindowAnalysis into the wire facets. Field-for-
// field and nothing else: no defaulting, no repair, no empty-collection
// substitution — a nil stays nil so `omitempty` keeps the key off the wire,
// which is what makes "nobody looked" distinguishable from "we looked and
// found nothing".
//
// The ONE function both row builders call, for the reason AnalysisFacets
// itself exists.
func facetsOf(a enrich.WindowAnalysis) AnalysisFacets {
	return AnalysisFacets{
		Workstreams:      a.Workstreams,
		Dynamics:         a.Dynamics,
		Effort:           a.Effort,
		PhysicalActs:     a.PhysicalActs,
		Files:            a.Files,
		Directories:      a.Directories,
		Components:       a.Components,
		HarnessTools:     a.HarnessTools,
		Programs:         a.Programs,
		ExternalSystems:  a.ExternalSystems,
		Integrations:     a.Integrations,
		NamedTerms:       a.NamedTerms,
		FileTypes:        a.FileTypes,
		ShellVerbs:       a.ShellVerbs,
		Subagents:        a.Subagents,
		McpServers:       a.McpServers,
		InventoryOmitted: a.InventoryOmitted,
		Prior:            a.Prior,
	}
}
