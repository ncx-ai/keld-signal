package settings

import (
	"encoding/json"
	"testing"
)

func TestClientTelemetryNilBlockReturnsAllDefaults(t *testing.T) {
	var c *ClientTelemetry
	eff := c.WithDefaults()
	want := EffectiveClientTelemetry{
		Enabled:          true,
		MinSeverity:      "warn",
		SampleRate:       1.0,
		GaugesEnabled:    true,
		GaugeIntervalS:   300,
		RSSThresholdMB:   4096,
		CPUThresholdPct:  150,
		SustainedWindowS: 120,
	}
	if eff != want {
		t.Fatalf("nil ClientTelemetry.WithDefaults() = %+v, want %+v", eff, want)
	}
	if !eff.Enabled {
		t.Fatal("Enabled must default to true (absent block = forward-compatible default-on)")
	}
}

func TestClientTelemetryFullBlockParsesAndOverridesAllDefaults(t *testing.T) {
	body := `{
		"client_telemetry": {
			"enabled": false,
			"min_severity": "error",
			"sample_rate": 0.25,
			"gauges_enabled": false,
			"gauge_interval_s": 60,
			"rss_threshold_mb": 2048,
			"cpu_threshold_pct": 90,
			"sustained_window_s": 30
		}
	}`
	var r Remote
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.ClientTelemetry == nil {
		t.Fatal("expected ClientTelemetry to be parsed, got nil")
	}
	eff := r.ClientTelemetry.WithDefaults()
	want := EffectiveClientTelemetry{
		Enabled:          false,
		MinSeverity:      "error",
		SampleRate:       0.25,
		GaugesEnabled:    false,
		GaugeIntervalS:   60,
		RSSThresholdMB:   2048,
		CPUThresholdPct:  90,
		SustainedWindowS: 30,
	}
	if eff != want {
		t.Fatalf("full-block WithDefaults() = %+v, want %+v", eff, want)
	}
}

func TestClientTelemetryPartialBlockFillsOnlyMissingFields(t *testing.T) {
	body := `{"client_telemetry": {"enabled": true, "sample_rate": 0.5}}`
	var r Remote
	if err := json.Unmarshal([]byte(body), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.ClientTelemetry == nil {
		t.Fatal("expected ClientTelemetry to be parsed, got nil")
	}
	eff := r.ClientTelemetry.WithDefaults()
	want := EffectiveClientTelemetry{
		Enabled:          true,
		MinSeverity:      "warn", // default (missing in partial block)
		SampleRate:       0.5,
		GaugesEnabled:    true, // default
		GaugeIntervalS:   300,  // default
		RSSThresholdMB:   4096, // default
		CPUThresholdPct:  150,  // default
		SustainedWindowS: 120,  // default
	}
	if eff != want {
		t.Fatalf("partial-block WithDefaults() = %+v, want %+v", eff, want)
	}
}

func TestRemoteAbsentClientTelemetryIsNil(t *testing.T) {
	var r Remote
	if err := json.Unmarshal([]byte(`{"include_entity_text": true}`), &r); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if r.ClientTelemetry != nil {
		t.Fatalf("expected nil ClientTelemetry when absent from JSON, got %+v", r.ClientTelemetry)
	}
	if !r.ClientTelemetry.WithDefaults().Enabled {
		t.Fatal("absent client_telemetry must still resolve to Enabled=true via nil-receiver defaults")
	}
}
