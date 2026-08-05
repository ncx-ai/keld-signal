package settings

// EnrichmentSchema is the org's ordered enrichment schema as served by
// keld-atlas GET /v1/enrichment-settings (distribution_schema). Only the org's
// CUSTOM passes are consumed here — built-in passes are compiled into the
// binary (see internal/agent/enrich). Absent block => no custom passes.
type EnrichmentSchema struct {
	Stages [][]string            `json:"stages"`
	Passes map[string]RemotePass `json:"passes"`
}

// RemotePass mirrors one entry of distribution_schema().passes.
type RemotePass struct {
	Key          string                   `json:"key"`
	Kind         string                   `json:"kind"` // single_label | multi_label | entity | structure | relation
	Title        string                   `json:"title"`
	Description  string                   `json:"description"`
	Labels       []RemoteLabel            `json:"labels"`
	ConditionOn  string                   `json:"condition_on"`
	LabelsByCond map[string][]RemoteLabel `json:"labels_by_cond"`
	MultiLabel   bool                     `json:"multi_label"`
	ClsThreshold *float64                 `json:"cls_threshold"`
	Version      string                   `json:"version"`
	// IsSystem marks one of the built-in passes compiled into this binary.
	// distribution_schema() serves built-ins and custom passes in the same flat
	// map, so this flag is the only thing distinguishing them.
	IsSystem bool `json:"is_system"`
}

// RemoteLabel is one label. Classification passes use ID/Text (+Description);
// entity passes use Label/Description (+optional Regex).
type RemoteLabel struct {
	ID          string `json:"id"`
	Text        string `json:"text"`
	Description string `json:"description"`
	Label       string `json:"label"`
	Regex       string `json:"regex"`
}
