package promptlog

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLogsPayloadShape(t *testing.T) {
	res := []kv{attr("service.name", "claude-code")}
	rec := logRecord{
		TimeUnixNano: "1", SeverityText: "INFO", Body: anyVal{StringValue: "claude_code.user_prompt"},
		Attributes: []kv{attr("event.name", "user_prompt"), attrInt("prompt_length", 19)},
	}
	b, err := logsPayload(res, []logRecord{rec})
	if err != nil {
		t.Fatal(err)
	}
	var p otlpLogs
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	if len(p.ResourceLogs) != 1 || p.ResourceLogs[0].Resource.Attributes[0].Key != "service.name" {
		t.Fatal("resource attr missing")
	}
	lr := p.ResourceLogs[0].ScopeLogs[0].LogRecords[0]
	if lr.Body.StringValue != "claude_code.user_prompt" {
		t.Fatal("body wrong")
	}
	// int attribute must serialize as intValue, not stringValue.
	if !strings.Contains(string(b), "\"intValue\"") {
		t.Fatalf("expected intValue in payload: %s", string(b))
	}
}

func TestMetricsPayloadShape(t *testing.T) {
	b, err := metricsPayload([]kv{attr("service.name", "claude-code")},
		[]metric{{Name: "claude_code.token.usage", Value: 42, IsInt: true, Attrs: []kv{attr("type", "output")}}})
	if err != nil {
		t.Fatal(err)
	}
	var m otlpMetrics
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if m.ResourceMetrics[0].ScopeMetrics[0].Metrics[0].Name != "claude_code.token.usage" {
		t.Fatal("metric name wrong")
	}
	if !strings.Contains(string(b), "\"asInt\"") {
		t.Fatalf("int metric should serialize asInt: %s", string(b))
	}
}

func TestMetricsPayloadGroupsByNameMonotonic(t *testing.T) {
	// Two datapoints for the SAME metric name (input/output) must produce ONE
	// metric with two datapoints (matching the captured CLI shape), not two
	// metrics with a duplicate name.
	b, err := metricsPayload(nil, []metric{
		{Name: "claude_code.token.usage", Value: 2, IsInt: true, Attrs: []kv{attr("type", "input")}},
		{Name: "claude_code.token.usage", Value: 4, IsInt: true, Attrs: []kv{attr("type", "output")}},
		{Name: "claude_code.cost.usage", Value: 0.08, IsInt: false},
	})
	if err != nil {
		t.Fatal(err)
	}
	var m otlpMetrics
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	ms := m.ResourceMetrics[0].ScopeMetrics[0].Metrics
	if len(ms) != 2 {
		t.Fatalf("expected 2 distinct metrics (token.usage, cost.usage), got %d", len(ms))
	}
	byName := map[string]otlpMetric{}
	for _, x := range ms {
		byName[x.Name] = x
	}
	if len(byName["claude_code.token.usage"].Sum.DataPoints) != 2 {
		t.Fatalf("token.usage must carry both datapoints, got %d", len(byName["claude_code.token.usage"].Sum.DataPoints))
	}
	if !byName["claude_code.token.usage"].Sum.IsMonotonic {
		t.Fatal("token.usage sum must be monotonic (matches CLI)")
	}
}
