package settings

// Remote is the org settings document served by keld-atlas. Fields are pointers
// so an absent key ("not set by the org") is distinct from an explicit false.
type Remote struct {
	IncludeEntityText *bool             `json:"include_entity_text"`
	ClientTelemetry   *ClientTelemetry  `json:"client_telemetry"`
	EnrichmentSchema  *EnrichmentSchema `json:"enrichment_schema"`
	// PIIRegions is the org's country-tier selection for PII detection (see
	// Settings.PIIRegions). A pointer so an absent key leaves the local value
	// alone, while an explicit [] means "universal tier only" — the two are
	// different answers and JSON can only tell them apart this way.
	//
	// Atlas does not serve this key yet. The client seam exists now so adopting
	// it later is a server change alone, rather than a second client migration.
	PIIRegions *[]string `json:"pii_regions"`
	// Features is the org's control over THE SIGNAL-EMBEDDINGS PATH: whether
	// feature vectors are collected, and whether they are published. Both
	// default OFF locally, and a nil block leaves both at the local value.
	//
	// Atlas does not serve this key yet. The client seam exists now so adopting
	// it later is a server change alone, rather than a second client migration
	// — the same reason PIIRegions above is already modelled.
	Features *Features `json:"features"`
	// Release is the org's control over WHICH RELEASE this machine runs — the
	// auto-update target. A nil block means NO UPDATES; see release.go for why
	// that is the strictest reading of the omitted-key rule in this file.
	Release *Release `json:"agent_release"`
	// Projects is the org's project-definition list for on-device block
	// attribution. A pointer so an absent key ("Atlas does not serve this
	// yet") is distinct from an explicit empty list.
	//
	// ⚠️ **ATLAS SERVES THIS NOW, and this comment used to say it did not.**
	// Verified against keld-atlas on 2026-09-04: `_org_settings` fills the key
	// from `services/workstream_attribution.wire_projects`, which POOLS every
	// authored workstream's VALUES into one flat list — deliberately, so
	// attribution runs one competition rather than one per workstream. Two
	// consequences the shape does not announce:
	//   - `Team` carries the WORKSTREAM'S NAME when a value has no owning team,
	//     so it is the only way to recover which bucket a value belongs to.
	//   - `Keywords` are the value's authored tags with their PREFIX STRIPPED:
	//     an admin types `repository: acme/web` and this field receives
	//     `acme/web`. A deterministic repo rule must therefore match by SHAPE,
	//     never by a prefix that does not survive the wire.
	// See internal/atlas.FromRemoteProjects, which regroups them, and
	// docs/v3/contracts.md for what is still missing (a write path).
	Projects *[]RemoteProject `json:"projects"`
}
