package settings

// ClientTelemetry is the org-governed control-plane block for the client
// events (Signal Client telemetry) capture+transport pipeline: whether it
// runs at all, how noisy it is, and the resource-watcher thresholds. Fields
// are pointers (like Remote's other fields) so an absent key ("not set by the
// org") is distinct from an explicit zero/false value, and a field added by a
// newer daemon against an older Atlas (or vice versa) degrades gracefully to
// its default rather than zeroing the whole block.
type ClientTelemetry struct {
	Enabled          *bool    `json:"enabled"`
	MinSeverity      *string  `json:"min_severity"`
	SampleRate       *float64 `json:"sample_rate"`
	GaugesEnabled    *bool    `json:"gauges_enabled"`
	GaugeIntervalS   *int     `json:"gauge_interval_s"`
	RSSThresholdMB   *float64 `json:"rss_threshold_mb"`
	CPUThresholdPct  *float64 `json:"cpu_threshold_pct"`
	SustainedWindowS *int     `json:"sustained_window_s"`
}

// EffectiveClientTelemetry is the fully-resolved (no-nil) form of
// ClientTelemetry, ready to feed the clientevents.Gate + resource.Thresholds
// constructors.
type EffectiveClientTelemetry struct {
	Enabled        bool
	MinSeverity    string
	SampleRate     float64
	GaugesEnabled  bool
	GaugeIntervalS int

	RSSThresholdMB   float64
	CPUThresholdPct  float64
	SustainedWindowS int
}

// defaultClientTelemetry is used to fill any nil field, and returned wholesale
// when the block itself is absent (nil receiver) — forward-compatible with an
// Atlas that predates this control-plane knob, or an org that hasn't
// configured it: client telemetry defaults ON.
var defaultClientTelemetry = EffectiveClientTelemetry{
	Enabled:          true,
	MinSeverity:      "warn",
	SampleRate:       1.0,
	GaugesEnabled:    true,
	GaugeIntervalS:   300,
	RSSThresholdMB:   4096,
	CPUThresholdPct:  150,
	SustainedWindowS: 120,
}

// WithDefaults resolves a possibly-nil/partial ClientTelemetry into concrete
// values, filling any nil field (or the whole block, if c is nil) from
// defaultClientTelemetry.
func (c *ClientTelemetry) WithDefaults() EffectiveClientTelemetry {
	eff := defaultClientTelemetry
	if c == nil {
		return eff
	}
	if c.Enabled != nil {
		eff.Enabled = *c.Enabled
	}
	if c.MinSeverity != nil {
		eff.MinSeverity = *c.MinSeverity
	}
	if c.SampleRate != nil {
		eff.SampleRate = *c.SampleRate
	}
	if c.GaugesEnabled != nil {
		eff.GaugesEnabled = *c.GaugesEnabled
	}
	if c.GaugeIntervalS != nil {
		eff.GaugeIntervalS = *c.GaugeIntervalS
	}
	if c.RSSThresholdMB != nil {
		eff.RSSThresholdMB = *c.RSSThresholdMB
	}
	if c.CPUThresholdPct != nil {
		eff.CPUThresholdPct = *c.CPUThresholdPct
	}
	if c.SustainedWindowS != nil {
		eff.SustainedWindowS = *c.SustainedWindowS
	}
	return eff
}
