package clientevents

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestSeverityRank(t *testing.T) {
	tests := []struct {
		sev      Severity
		wantRank int
	}{
		{SevInfo, 0},
		{SevWarn, 1},
		{SevError, 2},
		{SevCritical, 3},
		{Severity("unknown"), -1},
	}
	for _, tt := range tests {
		if got := tt.sev.rank(); got != tt.wantRank {
			t.Errorf("Severity(%q).rank() = %d, want %d", string(tt.sev), got, tt.wantRank)
		}
	}
}

func TestSeverityAtLeast(t *testing.T) {
	tests := []struct {
		sev  Severity
		min  Severity
		want bool
	}{
		{SevError, SevWarn, true},
		{SevInfo, SevWarn, false},
		{SevCritical, SevCritical, true},
		{SevWarn, SevWarn, true},
		{SevInfo, SevInfo, true},
		{SevWarn, SevError, false},
		{SevCritical, SevError, true},
		{Severity("unknown"), SevInfo, false},
		{SevInfo, Severity("unknown"), true},
	}
	for _, tt := range tests {
		if got := tt.sev.AtLeast(tt.min); got != tt.want {
			t.Errorf("Severity(%q).AtLeast(Severity(%q)) = %v, want %v", string(tt.sev), string(tt.min), got, tt.want)
		}
	}
}

func TestSchemaVersion(t *testing.T) {
	if SchemaVersion != 2 {
		t.Errorf("SchemaVersion = %d, want 2", SchemaVersion)
	}
}

func TestEventJSONOmitsEmptyFields(t *testing.T) {
	now := time.Now()
	evt := Event{
		Code:     "test.event",
		Severity: SevInfo,
		Fields:   nil,
		Corr: Corr{
			Org:       "org1",
			InstallID: "install123",
			RunID:     "run456",
			Version:   "1.0.0",
			OS:        "linux",
			Arch:      "amd64",
		},
		TS: now,
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// Check that "fields" key is NOT in the JSON when Fields is nil
	if strings.Contains(string(data), `"fields"`) {
		t.Errorf("marshaled JSON contains 'fields' key when Fields is nil: %s", string(data))
	}

	// Check that required keys are present
	requiredKeys := []string{`"code"`, `"severity"`, `"corr"`, `"ts"`}
	for _, key := range requiredKeys {
		if !strings.Contains(string(data), key) {
			t.Errorf("marshaled JSON missing key %s: %s", key, string(data))
		}
	}
}

func TestEventJSONOmitsEmptyFieldsWhenExplicitlyEmpty(t *testing.T) {
	now := time.Now()
	evt := Event{
		Code:     "test.event",
		Severity: SevWarn,
		Fields:   map[string]any{},
		Corr: Corr{
			Org:       "org1",
			InstallID: "install123",
			RunID:     "run456",
			Version:   "1.0.0",
			OS:        "linux",
			Arch:      "amd64",
		},
		TS: now,
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// Check that "fields" key is NOT in the JSON when Fields is empty map
	if strings.Contains(string(data), `"fields"`) {
		t.Errorf("marshaled JSON contains 'fields' key when Fields is empty map: %s", string(data))
	}
}

func TestEventJSONIncludesFieldsWhenPresent(t *testing.T) {
	now := time.Now()
	evt := Event{
		Code:     "test.event",
		Severity: SevError,
		Fields: map[string]any{
			"key1": "value1",
			"key2": 42,
		},
		Corr: Corr{
			Org:       "org1",
			InstallID: "install123",
			RunID:     "run456",
			Version:   "1.0.0",
			OS:        "linux",
			Arch:      "amd64",
		},
		TS: now,
	}

	data, err := json.Marshal(evt)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// Check that "fields" key IS in the JSON when Fields has content
	if !strings.Contains(string(data), `"fields"`) {
		t.Errorf("marshaled JSON missing 'fields' key when Fields has content: %s", string(data))
	}
}

func TestEventRoundTrip(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond) // JSON rounds timestamps
	original := Event{
		Code:     "test.event",
		Severity: SevCritical,
		Fields: map[string]any{
			"error": "something broke",
		},
		Corr: Corr{
			Org:       "acme",
			Actor:     "user123",
			InstallID: "install789",
			RunID:     "run999",
			SessionID: "session111",
			PromptID:  "prompt222",
			Version:   "1.2.3",
			OS:        "darwin",
			Arch:      "arm64",
		},
		TS: now,
	}

	// Marshal
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("json.Marshal failed: %v", err)
	}

	// Unmarshal into new struct
	var roundTripped Event
	if err := json.Unmarshal(data, &roundTripped); err != nil {
		t.Fatalf("json.Unmarshal failed: %v", err)
	}

	// Verify all fields match
	if roundTripped.Code != original.Code {
		t.Errorf("Code mismatch: got %q, want %q", roundTripped.Code, original.Code)
	}
	if roundTripped.Severity != original.Severity {
		t.Errorf("Severity mismatch: got %q, want %q", roundTripped.Severity, original.Severity)
	}
	if !roundTripped.TS.Equal(original.TS) {
		t.Errorf("TS mismatch: got %v, want %v", roundTripped.TS, original.TS)
	}
	if roundTripped.Corr != original.Corr {
		t.Errorf("Corr mismatch: got %+v, want %+v", roundTripped.Corr, original.Corr)
	}

	// Fields round-trip
	if len(roundTripped.Fields) != len(original.Fields) {
		t.Errorf("Fields length mismatch: got %d, want %d", len(roundTripped.Fields), len(original.Fields))
	}
	for k, v := range original.Fields {
		if roundTripped.Fields[k] != v {
			t.Errorf("Fields[%q] mismatch: got %v, want %v", k, roundTripped.Fields[k], v)
		}
	}
}
