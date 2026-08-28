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
}
