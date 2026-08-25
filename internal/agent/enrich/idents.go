package enrich

// NameCount is one entry of a window's IDENTIFIER-shaped inventories —
// `harness_tools`, `programs`, `external_systems` and `integrations` (see
// Profile.HarnessTools/Programs/ExternalSystems/Integrations) — which all
// share this shape: a bare token and how many times the window's hour
// referenced it.
//
// It is a COUNT, not a share, for the same reason Act and PathCount are: these
// are inventories (what was used), not allocations (what owns the window), so
// there is no denominator to divide by.
//
// Like PathCount and UNLIKE Act, the vocabulary at each of these four levels is
// OPEN — a harness tool name, a shell program, a hostname or an MCP tool id is
// not a member of a closed table — so there is no KnownX gate at the decode
// boundary either. What stands in its place is a per-dimension STRUCTURAL
// gate, applied per entry at the sidecar decode boundary
// (sidecar.convertIdentifierInventory / convertProgramInventory /
// convertExternalSystemInventory), not a vocabulary lookup:
//
//   - harness_tools / integrations: bare IDENTIFIER SHAPE. The harness's own
//     tool set genuinely grows (ToolSearch, Artifact and SendMessage are all
//     recent additions), so this is deliberately NOT a hardcoded allowlist — a
//     stale one would silently drop a legitimate new tool, which is worse than
//     forwarding an identifier a shape gate already bounds.
//   - programs: identifier shape PLUS a rejection of anything containing a
//     path separator or starting with a leading dot. The measured defect this
//     closes: `.env.example` — a filename, not a program — reaching the
//     sidecar's bashlex-based exe extraction.
//   - external_systems: rejects bare IP LITERALS (v4 and v6) and otherwise
//     keeps the value whole, INCLUDING internal and corporate hostnames — see
//     sidecar.convertExternalSystemInventory for why that is a deliberate,
//     argued decision rather than an oversight.
type NameCount struct {
	// Value is the bare identifier: a harness tool name, a shell program name,
	// a hostname/service identifier, or an MCP tool id. Never empty.
	Value string `json:"value"`
	// N is how many times the window referenced it. Always stated.
	N int `json:"n"`
}
