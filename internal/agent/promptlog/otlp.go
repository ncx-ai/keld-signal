package promptlog

import (
	"encoding/json"
	"strconv"
)

// --- OTLP/HTTP JSON types (ExportLogs/MetricsServiceRequest subset) ---

type otlpLogs struct {
	ResourceLogs []resourceLogs `json:"resourceLogs"`
}
type resourceLogs struct {
	Resource  otlpResource `json:"resource"`
	ScopeLogs []scopeLogs  `json:"scopeLogs"`
}
type otlpResource struct {
	Attributes []kv `json:"attributes"`
}
type scopeLogs struct {
	Scope      otlpScope   `json:"scope"`
	LogRecords []logRecord `json:"logRecords"`
}
type otlpScope struct {
	Name string `json:"name"`
}
type logRecord struct {
	TimeUnixNano         string `json:"timeUnixNano,omitempty"`
	ObservedTimeUnixNano string `json:"observedTimeUnixNano,omitempty"`
	SeverityNumber       int    `json:"severityNumber,omitempty"`
	SeverityText         string `json:"severityText,omitempty"`
	Body                 anyVal `json:"body"`
	Attributes           []kv   `json:"attributes"`
}
type kv struct {
	Key   string `json:"key"`
	Value anyVal `json:"value"`
}

// anyVal is an OTLP AnyValue. Only one field is set per value.
type anyVal struct {
	StringValue string  `json:"stringValue,omitempty"`
	IntValue    otlpInt `json:"intValue,omitempty"`
}

// otlpInt is an OTLP integer attribute value.
//
// ⚠️ **OTLP/JSON PERMITS BOTH A DECIMAL STRING AND A BARE NUMBER FOR AN
// `intValue`, AND THE REAL TOOLS USE BOTH.** This was a plain `string`, which is
// what the protobuf-JSON mapping prescribes and what this package emits — but a
// captured `claude_code.api_request` record writes `"event.sequence":
// {"intValue": 237}` as a NUMBER, so decoding a real payload with the strict type
// failed outright ("cannot unmarshal number into Go struct field ... of type
// string"). Reading is therefore tolerant and writing stays strict: a value is
// accepted in either form and always re-emitted as the decimal string, so a
// mirrored payload is byte-stable regardless of what it was compared against.
type otlpInt string

func (o *otlpInt) UnmarshalJSON(b []byte) error {
	if len(b) >= 2 && b[0] == '"' && b[len(b)-1] == '"' {
		b = b[1 : len(b)-1]
	}
	if string(b) == "null" {
		b = nil
	}
	*o = otlpInt(b)
	return nil
}

func (o otlpInt) MarshalJSON() ([]byte, error) {
	return json.Marshal(string(o))
}

type otlpMetrics struct {
	ResourceMetrics []resourceMetrics `json:"resourceMetrics"`
}
type resourceMetrics struct {
	Resource     otlpResource   `json:"resource"`
	ScopeMetrics []scopeMetrics `json:"scopeMetrics"`
}
type scopeMetrics struct {
	Scope   otlpScope    `json:"scope"`
	Metrics []otlpMetric `json:"metrics"`
}
type otlpMetric struct {
	Name string  `json:"name"`
	Sum  otlpSum `json:"sum"`
}
type otlpSum struct {
	DataPoints             []numberDP `json:"dataPoints"`
	AggregationTemporality int        `json:"aggregationTemporality"`
	IsMonotonic            bool       `json:"isMonotonic"`
}
type numberDP struct {
	AsInt        string  `json:"asInt,omitempty"`
	AsDouble     float64 `json:"asDouble,omitempty"`
	TimeUnixNano string  `json:"timeUnixNano,omitempty"`
	Attributes   []kv    `json:"attributes,omitempty"`
}

// metric is the caller-facing view of one datapoint; metricsPayload turns it into
// a single-datapoint OTLP Sum.
type metric struct {
	Name         string
	Value        float64
	IsInt        bool
	Attrs        []kv
	TimeUnixNano string
}

// attr builds a string-valued OTLP attribute.
func attr(k, v string) kv { return kv{Key: k, Value: anyVal{StringValue: v}} }

// attrInt builds an integer-valued OTLP attribute (encoded as a decimal string).
func attrInt(k string, n int) kv { return kv{Key: k, Value: anyVal{IntValue: otlpInt(itoa(n))}} }

// itoa is strconv.Itoa under a shorter name, used wherever an id is composed
// from a record's own ordinal.
func itoa(n int) string { return strconv.Itoa(n) }

// logsPayload marshals an OTLP/HTTP logs export request for one resource.
func logsPayload(res []kv, records []logRecord) ([]byte, error) {
	return json.Marshal(otlpLogs{ResourceLogs: []resourceLogs{{
		Resource:  otlpResource{Attributes: res},
		ScopeLogs: []scopeLogs{{Scope: otlpScope{Name: scopeName}, LogRecords: records}},
	}}})
}

// metricsPayload marshals an OTLP/HTTP metrics export request for one resource.
// Datapoints are grouped under one Sum per metric NAME (matching the captured CLI
// shape: e.g. claude_code.token.usage carries one datapoint per token type), with
// delta temporality and monotonic=true — the same encoding the CLI emits.
func metricsPayload(res []kv, metrics []metric) ([]byte, error) {
	order := []string{}
	dps := map[string][]numberDP{}
	for _, m := range metrics {
		dp := numberDP{TimeUnixNano: m.TimeUnixNano, Attributes: m.Attrs}
		if m.IsInt {
			dp.AsInt = strconv.FormatInt(int64(m.Value), 10)
		} else {
			dp.AsDouble = m.Value
		}
		if _, seen := dps[m.Name]; !seen {
			order = append(order, m.Name)
		}
		dps[m.Name] = append(dps[m.Name], dp)
	}
	ms := make([]otlpMetric, 0, len(order))
	for _, name := range order {
		ms = append(ms, otlpMetric{
			Name: name,
			Sum:  otlpSum{DataPoints: dps[name], AggregationTemporality: 1 /*delta*/, IsMonotonic: true},
		})
	}
	return json.Marshal(otlpMetrics{ResourceMetrics: []resourceMetrics{{
		Resource:     otlpResource{Attributes: res},
		ScopeMetrics: []scopeMetrics{{Scope: otlpScope{Name: scopeName}, Metrics: ms}},
	}}})
}

// scopeName is the instrumentation scope keld stamps on emitted telemetry.
const scopeName = "keld-agent/watch"
