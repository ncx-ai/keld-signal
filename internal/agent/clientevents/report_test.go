package clientevents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The two plants. AC-6's whole point: a report bundle is the one thing a
// person deliberately sends us, so it is the one place a leak would be both
// invisible and consented to.
const (
	plantedPrompt = "refactor the billing service so Acme Corp stops being charged twice for annual seats"
	plantedKey    = "sk-ant-PLANTEDKEY0123456789abcdefghijklmnopqrstuv"
)

func seededLog() []string {
	return []string{
		"2026/09/15 12:00:00 keld-agent: listening on 127.0.0.1:52928",
		"2026/09/15 12:00:01 keld-agent: enrich failed for prompt: " + plantedPrompt,
		"2026/09/15 12:00:02 keld-agent: " + plantedKey,
		"2026/09/15 12:00:03 keld-agent: auth: " + plantedKey,
		"2026/09/15 12:00:04 keld-agent: waiting for /Users/someone/.keld/hook.json",
		"2026/09/15 12:00:05 sidecar: ready",
	}
}

func sampleInput() BundleInput {
	return BundleInput{
		Source:         "codex",
		State:          "broken",
		ToolVersion:    "0.153.4",
		AgentVersion:   "3.0.0-rc.1",
		SidecarVersion: "3.0.0-rc.1",
		OS:             "darwin",
		Arch:           "arm64",
		InstallID:      "a3f9c2e1b8d04f7a9c1e2b3d4f5a6b7c",
		DoctorFindings: []string{"telemetry_stale", "hook_untrusted"},
		Counters:       map[string]int{"codex.hook": 0, "codex.otel": 41, "codex.watcher": 2},
		Log:            seededLog(),
	}
}

// TestNeitherPlantReachesTheBundleOrTheEvent is AC-6.
func TestNeitherPlantReachesTheBundleOrTheEvent(t *testing.T) {
	dir := t.TempDir()
	b := BuildBundle(time.Date(2026, 9, 15, 12, 5, 0, 0, time.UTC), sampleInput())

	path, err := WriteBundle(dir, b)
	if err != nil {
		t.Fatal(err)
	}
	onDisk, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// The event as it would actually be published: through the redaction gate,
	// marshalled exactly as the reporter marshals it.
	ev := Event{Code: "integration.report", Severity: SevWarn, Fields: redactFields(b.EventFields())}
	wire, err := json.Marshal(ev)
	if err != nil {
		t.Fatal(err)
	}

	for _, plant := range []string{plantedPrompt, plantedKey} {
		if strings.Contains(string(onDisk), plant) {
			t.Errorf("the on-disk bundle carries a planted string:\n%s", onDisk)
		}
		if strings.Contains(string(wire), plant) {
			t.Errorf("the queued event carries a planted string:\n%s", wire)
		}
	}
	// A fragment of the prompt is a leak too — assert on a distinctive word,
	// not only the whole sentence.
	for _, frag := range []string{"Acme", "billing service", "PLANTEDKEY"} {
		if strings.Contains(string(onDisk), frag) || strings.Contains(string(wire), frag) {
			t.Errorf("a fragment %q survived", frag)
		}
	}
}

// TestABareVendorTokenIsDroppedEvenWithNoKeywordBesideIt — measured, not
// assumed: creddetect's generic-api-key rule has a KEYWORD pre-filter, so
// `sk-ant-…` alone on a line scores nothing while `auth: sk-ant-…` is caught.
// The prefix backstop exists for exactly that line.
func TestABareVendorTokenIsDroppedEvenWithNoKeywordBesideIt(t *testing.T) {
	for _, line := range []string{
		"2026/09/15 12:00:02 keld-agent: " + plantedKey,
		"2026/09/15 12:00:02 keld-agent: ghp_PLANTEDKEY0123456789abcdefghijklmnop",
		"2026/09/15 12:00:02 keld-agent: AKIAIOSFODNN7EXAMPLE",
		"2026/09/15 12:00:02 keld-agent: AIzaSyPLANTEDKEY0123456789abcdefghijklm",
	} {
		if _, kept, _ := scrubLogLine(line); kept {
			t.Errorf("kept a credential line: %q", line)
		}
	}
}

// TestAShortOperationalLineSurvives — the scrubber must not be a shredder. If
// every line comes back "<redacted>" the bundle is worthless and nobody will
// ask for one twice.
func TestAShortOperationalLineSurvives(t *testing.T) {
	line := "2026/09/15 12:00:00 keld-agent: listening on 127.0.0.1:52928"
	got, kept, redacted := scrubLogLine(line)
	if !kept || redacted {
		t.Fatalf("scrubLogLine(%q) = (%q, kept=%v, redacted=%v)", line, got, kept, redacted)
	}
	if !strings.Contains(got, "listening on 127.0.0.1:52928") {
		t.Fatalf("message half did not survive: %q", got)
	}
	if !strings.HasPrefix(got, "2026/09/15 12:00:00 ") {
		t.Fatalf("timestamp did not survive: %q", got)
	}
}

// TestAPathIsReplacedNotTheWholeLine — RedactError's rule, so "waiting for
// <path>" stays a readable fact.
func TestAPathIsReplacedNotTheWholeLine(t *testing.T) {
	got, kept, _ := scrubLogLine("2026/09/15 12:00:04 keld-agent: waiting for /Users/someone/.keld/hook.json")
	if !kept {
		t.Fatal("dropped")
	}
	if strings.Contains(got, "/Users/someone") {
		t.Fatalf("path survived: %q", got)
	}
	if !strings.Contains(got, "<path>") {
		t.Fatalf("path was not replaced: %q", got)
	}
}

// TestWhatWasCutIsDECLARED — the omittedNotice rule. A scrubbed log and a
// short one must not look alike.
func TestWhatWasCutIsDECLARED(t *testing.T) {
	b := BuildBundle(time.Now(), sampleInput())
	if b.LogDropped != 2 {
		t.Errorf("LogDropped = %d, want 2 (the two credential lines)", b.LogDropped)
	}
	if b.LogRedacted != 1 {
		t.Errorf("LogRedacted = %d, want 1 (the prompt line)", b.LogRedacted)
	}
	if b.LogKept != 4 {
		t.Errorf("LogKept = %d, want 4", b.LogKept)
	}
	if b.LogKept != len(b.Log) {
		t.Errorf("LogKept %d disagrees with len(Log) %d", b.LogKept, len(b.Log))
	}
	f := b.EventFields()
	for _, k := range []string{"log_kept", "log_redacted", "log_dropped"} {
		if _, ok := f[k]; !ok {
			t.Errorf("event fields do not declare %q", k)
		}
	}
}

// TestTheLogIsCappedAtTwoHundredLinesAndKeepsTheNEWEST — a report is about
// what just went wrong.
func TestTheLogIsCappedAtTwoHundredLinesAndKeepsTheNEWEST(t *testing.T) {
	in := sampleInput()
	in.Log = nil
	for i := 0; i < 600; i++ {
		in.Log = append(in.Log, fmt.Sprintf("2026/09/15 12:00:00 keld-agent: line %d", i))
	}
	b := BuildBundle(time.Now(), in)
	if len(b.Log) != maxReportLogLines {
		t.Fatalf("kept %d lines, want %d", len(b.Log), maxReportLogLines)
	}
	if !strings.Contains(b.Log[len(b.Log)-1], "line 599") {
		t.Fatalf("newest line missing, last is %q", b.Log[len(b.Log)-1])
	}
}

// TestTheEventCarriesNoLogLinesAtAll is spec gap 4's answer, and it is
// STRUCTURAL rather than a cap: redactFields drops every non-primitive value,
// so a []string of log lines cannot ride a client event even if someone added
// one. The lines live in the on-disk bundle; the event carries the counts and
// the file name.
func TestTheEventCarriesNoLogLinesAtAll(t *testing.T) {
	b := BuildBundle(time.Now(), sampleInput())
	fields := redactFields(b.EventFields())
	for k, v := range fields {
		if _, isSlice := v.([]string); isSlice {
			t.Errorf("fields[%q] is a slice", k)
		}
	}
	wire, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "listening on") {
		t.Errorf("a log line rode the event: %s", wire)
	}
}

// TestTheBundleAndTheEventAreBothMeasuredAndSmall — spec gap 4 asked for a
// measurement, so this test IS the measurement and fails if either grows past
// its stated bound.
func TestTheBundleAndTheEventAreBothMeasuredAndSmall(t *testing.T) {
	in := sampleInput()
	in.Log = nil
	// A worst case: 200 lines at the scrubber's own per-line rune cap.
	long := "2026/09/15 12:00:00 keld-agent: " + strings.Repeat("x", 400)
	for i := 0; i < maxReportLogLines+50; i++ {
		in.Log = append(in.Log, long)
	}
	b := BuildBundle(time.Now(), in)

	raw, err := json.MarshalIndent(b, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	fields, err := json.Marshal(redactFields(b.EventFields()))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MEASURED: bundle %d bytes on disk, event fields %d bytes on the wire (%d log lines)",
		len(raw), len(fields), len(b.Log))

	if len(raw) > maxReportBytes {
		t.Fatalf("bundle is %d bytes, over the %d cap", len(raw), maxReportBytes)
	}
	if len(fields) > 4096 {
		t.Fatalf("event fields are %d bytes — a report must never dominate a batch", len(fields))
	}
}

// TestWriteBundleNamesTheFileByInstantAndSource and keeps it user-only.
func TestWriteBundleNamesTheFileByInstantAndSource(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reports")
	b := BuildBundle(time.Date(2026, 9, 15, 12, 5, 6, 0, time.UTC), sampleInput())
	path, err := WriteBundle(dir, b)
	if err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(path); got != "20260915T120506Z-codex.json" {
		t.Fatalf("file name = %q", got)
	}
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
	var back Bundle
	raw, _ := os.ReadFile(path)
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("bundle is not valid JSON: %v", err)
	}
	if back.Source != "codex" || back.Versions["agent"] != "3.0.0-rc.1" {
		t.Fatalf("round trip lost content: %+v", back)
	}
}

// TestDoctorFindingsAreNAMEDNotQUOTED — a finding is an id, and an id that
// reads as prose is redacted like anything else.
func TestDoctorFindingsAreNAMEDNotQUOTED(t *testing.T) {
	in := sampleInput()
	in.DoctorFindings = []string{"telemetry_stale", "the codex session started before its config was written at " + plantedPrompt}
	b := BuildBundle(time.Now(), in)
	raw, _ := json.Marshal(b)
	if strings.Contains(string(raw), "Acme") {
		t.Fatalf("a prose finding was quoted verbatim: %s", raw)
	}
	if b.DoctorFindings[0] != "telemetry_stale" {
		t.Fatalf("a short finding id did not survive: %+v", b.DoctorFindings)
	}
}

// TestTailLinesReadsTheEndOfARealFile — and an absent log is an empty list,
// never an error: a machine whose service manager writes no log file still has
// a problem worth reporting.
func TestTailLinesReadsTheEndOfARealFile(t *testing.T) {
	p := filepath.Join(t.TempDir(), "agent.err.log")
	var sb strings.Builder
	for i := 0; i < 50; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	if err := os.WriteFile(p, []byte(sb.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	got := TailLines(p, 10)
	if len(got) != 10 || got[9] != "line 49" || got[0] != "line 40" {
		t.Fatalf("tail = %+v", got)
	}
	if got := TailLines(filepath.Join(t.TempDir(), "nope.log"), 10); len(got) != 0 {
		t.Fatalf("absent file = %+v, want empty", got)
	}
}

// TestARedactedLineStillSaysWhenAndWhichComponent — the survival claim the
// design rests on, pinned. Measured on a real ~/.keld/agent.log: 89 of 90
// message halves are replaced, so if the instant and the component went with
// them the bundle would be 90 identical lines and worth nothing. What is left
// is a per-component timeline, which is a real reading.
func TestARedactedLineStillSaysWhenAndWhichComponent(t *testing.T) {
	// The debug log's own shape, taken verbatim from a running daemon.
	line := "2026-09-15T14:50:30Z attrib: quarantined job session=s1 start=1 after 4 attempts (sidecar /attribute call failed)"
	got, kept, redacted := scrubLogLine(line)
	if !kept || !redacted {
		t.Fatalf("scrubLogLine = (%q, kept=%v, redacted=%v)", got, kept, redacted)
	}
	if got != "2026-09-15T14:50:30Z attrib: <redacted>" {
		t.Fatalf("got %q — the instant and the component must survive a redacted message", got)
	}
}

// TestBothDaemonLogShapesAreRecognised — agent.err.log is the Go standard
// logger, agent.log is RFC 3339. A reader that knew only one threw the other's
// instant away.
func TestBothDaemonLogShapesAreRecognised(t *testing.T) {
	for _, tc := range []struct{ line, wantPrefix string }{
		{"2026/09/14 14:28:08 keld-agent: ready", "2026/09/14 14:28:08 keld-agent: "},
		{"2026-09-15T14:26:10Z promptlog: ready", "2026-09-15T14:26:10Z promptlog: "},
		{"2026-09-15T14:50:30Z ingest signal: ready", "2026-09-15T14:50:30Z ingest signal: "},
	} {
		got, kept, _ := scrubLogLine(tc.line)
		if !kept || !strings.HasPrefix(got, tc.wantPrefix) {
			t.Errorf("scrubLogLine(%q) = %q, want prefix %q", tc.line, got, tc.wantPrefix)
		}
	}
}
