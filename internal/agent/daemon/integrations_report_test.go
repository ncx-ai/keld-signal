package daemon

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/integrations"
)

type recordingSink struct {
	codes  []string
	fields []map[string]any
}

func (r *recordingSink) Emit(code, severity string, fields map[string]any) {
	r.codes = append(r.codes, code)
	r.fields = append(r.fields, fields)
}
func (r *recordingSink) EmitExempt(code, severity string, fields map[string]any) {
	r.Emit(code, severity, fields)
}

func reportMux(t *testing.T, deps reportDeps) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	integrationsReportRoute(deps)(mux, func(h http.Handler) http.Handler { return h })
	return mux
}

func postReport(t *testing.T, mux *http.ServeMux, id string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/integrations/"+id+"/report", nil))
	return rr
}

const (
	routePlantedPrompt = "rewrite the invoicing job so Northwind Traders is billed once per seat per month"
	routePlantedKey    = "sk-ant-PLANTEDKEY0123456789abcdefghijklmnopqrstuv"
)

// TestReportWritesABundleAndQueuesTheEvent is AC-6's happy path, end to end
// through the route a person's click reaches.
func TestReportWritesABundleAndQueuesTheEvent(t *testing.T) {
	dir := t.TempDir()
	sink := &recordingSink{}
	mux := reportMux(t, reportDeps{
		ReportsDir: filepath.Join(dir, "reports"),
		Log:        func() []string { return []string{"2026/09/15 12:00:00 keld-agent: listening on 127.0.0.1:52928"} },
		Describe: func(id string) (string, string, []string, bool) {
			return "broken", "0.153.4", []string{"telemetry_stale"}, true
		},
		Counters:       func(string) map[string]int { return map[string]int{"codex.otel": 41} },
		Sink:           sink,
		AgentVersion:   "3.0.0-rc.1",
		SidecarVersion: "3.0.0-rc.1",
		InstallID:      "a3f9c2e1",
		Now:            func() time.Time { return time.Date(2026, 9, 15, 12, 5, 6, 0, time.UTC) },
	})

	rr := postReport(t, mux, "codex")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	path, _ := body["report_path"].(string)
	if path == "" {
		t.Fatalf("no report_path: %s", rr.Body.String())
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("bundle not on disk: %v", err)
	}
	if body["event_queued"] != true {
		t.Errorf("event_queued = %v", body["event_queued"])
	}
	if len(sink.codes) != 1 || sink.codes[0] != integrations.CodeReport {
		t.Fatalf("emitted %+v, want one %s", sink.codes, integrations.CodeReport)
	}
	if sink.fields[0]["source"] != "codex" || sink.fields[0]["state"] != "broken" {
		t.Errorf("event fields = %+v", sink.fields[0])
	}
}

// TestReportCarriesNeitherAPromptNorACredential is AC-6's point, asserted at
// the route rather than at the builder: this is the path a person actually
// triggers, over a log file on disk.
func TestReportCarriesNeitherAPromptNorACredential(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "agent.err.log")
	seeded := strings.Join([]string{
		"2026/09/15 12:00:00 keld-agent: listening on 127.0.0.1:52928",
		"2026/09/15 12:00:01 keld-agent: enrich failed for prompt: " + routePlantedPrompt,
		"2026/09/15 12:00:02 keld-agent: " + routePlantedKey,
		"2026/09/15 12:00:03 sidecar: ready",
	}, "\n") + "\n"
	if err := os.WriteFile(logPath, []byte(seeded), 0o600); err != nil {
		t.Fatal(err)
	}

	sink := &recordingSink{}
	mux := reportMux(t, reportDeps{
		ReportsDir: filepath.Join(dir, "reports"),
		Log:        func() []string { return tailForTest(t, logPath) },
		Sink:       sink,
		Now:        func() time.Time { return time.Date(2026, 9, 15, 12, 5, 6, 0, time.UTC) },
	})

	rr := postReport(t, mux, "codex")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	onDisk, err := os.ReadFile(body["report_path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	wire, err := json.Marshal(sink.fields)
	if err != nil {
		t.Fatal(err)
	}

	for _, plant := range []string{routePlantedPrompt, routePlantedKey, "Northwind", "PLANTEDKEY", "invoicing"} {
		if strings.Contains(string(onDisk), plant) {
			t.Errorf("the on-disk bundle carries %q:\n%s", plant, onDisk)
		}
		if strings.Contains(string(wire), plant) {
			t.Errorf("the queued event carries %q:\n%s", plant, wire)
		}
	}
	// And it is not empty: a bundle that dropped everything would pass the
	// check above while being useless.
	if !strings.Contains(string(onDisk), "listening on 127.0.0.1:52928") {
		t.Errorf("the bundle kept nothing useful:\n%s", onDisk)
	}
}

// TestAnUnknownToolIsRefusedNotAccepted — a report filed against a tool this
// catalogue has never heard of would reach Atlas joinable to nothing.
func TestAnUnknownToolIsRefusedNotAccepted(t *testing.T) {
	sink := &recordingSink{}
	mux := reportMux(t, reportDeps{ReportsDir: t.TempDir(), Log: func() []string { return nil }, Sink: sink})
	rr := postReport(t, mux, "emacs")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if len(sink.codes) != 0 {
		t.Fatalf("emitted %+v for an unknown tool", sink.codes)
	}
}

// TestAGetIsNotAReport — the pattern is method-scoped, so a GET does not fall
// through to a handler that would write a file.
func TestAGetIsNotAReport(t *testing.T) {
	mux := reportMux(t, reportDeps{ReportsDir: t.TempDir(), Log: func() []string { return nil }})
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/v1/integrations/codex/report", nil))
	if rr.Code == http.StatusOK {
		t.Fatalf("a GET produced a report: %d", rr.Code)
	}
}

// TestWithNoStateRuleWiredTheReportSaysSo — the daemon must still take a
// report, and must name the gap rather than publish an empty state as if it
// were an answer.
func TestWithNoStateRuleWiredTheReportSaysSo(t *testing.T) {
	dir := t.TempDir()
	sink := &recordingSink{}
	mux := reportMux(t, reportDeps{
		ReportsDir: filepath.Join(dir, "reports"),
		Log:        func() []string { return nil },
		Sink:       sink,
		Now:        func() time.Time { return time.Date(2026, 9, 15, 12, 5, 6, 0, time.UTC) },
	})
	rr := postReport(t, mux, "claude_code")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body.String())
	}
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	raw, err := os.ReadFile(body["report_path"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "state_rule_unavailable") {
		t.Fatalf("the gap was not named:\n%s", raw)
	}
}

// TestTheRouteIsAnIngressRouteBehindTheSharedSecret — it is mounted the way
// every other loopback route is, so it cannot get authentication subtly
// different.
func TestTheRouteIsAnIngressRouteBehindTheSharedSecret(t *testing.T) {
	var r ingress.Route = integrationsReportRoute(reportDeps{})
	mux := http.NewServeMux()
	r(mux, ingress.RequireSecret("s3cret"))
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/v1/integrations/codex/report", nil))
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status without the secret = %d, want 401", rr.Code)
	}
}

func tailForTest(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
}
