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

// anyVal is an OTLP AnyValue. OTLP/JSON encodes integers as decimal strings under
// "intValue"; only one field is set per value.
type anyVal struct {
	StringValue string `json:"stringValue,omitempty"`
	IntValue    string `json:"intValue,omitempty"`
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
func attrInt(k string, n int) kv { return kv{Key: k, Value: anyVal{IntValue: strconv.Itoa(n)}} }

// logsPayload marshals an OTLP/HTTP logs export request for one resource.
func logsPayload(res []kv, records []logRecord) ([]byte, error) {
	return json.Marshal(otlpLogs{ResourceLogs: []resourceLogs{{
		Resource:  otlpResource{Attributes: res},
		ScopeLogs: []scopeLogs{{Scope: otlpScope{Name: scopeName}, LogRecords: records}},
	}}})
}

// metricsPayload marshals an OTLP/HTTP metrics export request for one resource.
// Each metric becomes a single-datapoint, non-monotonic Sum (delta temporality).
func metricsPayload(res []kv, metrics []metric) ([]byte, error) {
	ms := make([]otlpMetric, 0, len(metrics))
	for _, m := range metrics {
		dp := numberDP{TimeUnixNano: m.TimeUnixNano, Attributes: m.Attrs}
		if m.IsInt {
			dp.AsInt = strconv.FormatInt(int64(m.Value), 10)
		} else {
			dp.AsDouble = m.Value
		}
		ms = append(ms, otlpMetric{
			Name: m.Name,
			Sum:  otlpSum{DataPoints: []numberDP{dp}, AggregationTemporality: 1 /*delta*/, IsMonotonic: false},
		})
	}
	return json.Marshal(otlpMetrics{ResourceMetrics: []resourceMetrics{{
		Resource:     otlpResource{Attributes: res},
		ScopeMetrics: []scopeMetrics{{Scope: otlpScope{Name: scopeName}, Metrics: ms}},
	}}})
}

// scopeName is the instrumentation scope keld stamps on emitted telemetry.
const scopeName = "keld-agent/watch"
