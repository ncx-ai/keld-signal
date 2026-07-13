package settings

// Remote is the org settings document served by keld-atlas. Fields are pointers
// so an absent key ("not set by the org") is distinct from an explicit false.
type Remote struct {
	IncludeEntityText *bool            `json:"include_entity_text"`
	ClientTelemetry   *ClientTelemetry `json:"client_telemetry"`
}
