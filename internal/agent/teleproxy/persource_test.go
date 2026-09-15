package teleproxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSourceOfEveryCapturedToolPayload is the gap-3 answer, read off REAL
// payloads rather than guessed: each fixture under testdata/ was captured by
// pointing the tool's own OTLP exporter at a loopback listener and running one
// prompt. See testdata/README.md for the recipe and the redaction.
func TestSourceOfEveryCapturedToolPayload(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"otlp-claude-code-logs.json", "claude_code"},
		{"otlp-codex-logs.json", "codex"},
		{"otlp-codex-metrics.json", "codex"},
		{"otlp-gemini-cli-logs.json", "gemini_cli"},
		// The payload the striptext work captured, kept as a second Claude
		// Code reading from a different machine and a much older build.
		{"claude_code_logs.json", "claude_code"},
	} {
		body, err := os.ReadFile(filepath.Join("testdata", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		if got := SourceOf(body); got != tc.want {
			t.Errorf("SourceOf(%s) = %q, want %q", tc.file, got, tc.want)
		}
	}
}

// TestServiceNameIsWhatTheToolsActuallySend pins the three literals the
// fixtures carry, so a mapping edit that no longer matches production fails
// here rather than silently sending every forward to the unknown bucket.
func TestServiceNameIsWhatTheToolsActuallySend(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"otlp-claude-code-logs.json", "claude-code"},
		{"otlp-codex-logs.json", "codex_exec"},
		{"otlp-gemini-cli-logs.json", "gemini-cli"},
	} {
		body, err := os.ReadFile(filepath.Join("testdata", tc.file))
		if err != nil {
			t.Fatal(err)
		}
		if got := ServiceName(body); got != tc.want {
			t.Errorf("ServiceName(%s) = %q, want %q", tc.file, got, tc.want)
		}
	}
}

// TestCodexEntrypointsAreOneSource — `codex exec` sends codex_exec, and the
// TUI and the MCP server send their own names. They are one integration.
func TestCodexEntrypointsAreOneSource(t *testing.T) {
	for _, name := range []string{"codex_exec", "codex_tui", "codex_mcp_server", "codex"} {
		body := []byte(`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"` + name + `"}}]}}]}`)
		if got := SourceOf(body); got != "codex" {
			t.Errorf("SourceOf(service.name=%q) = %q, want codex", name, got)
		}
	}
}

// TestAnUnrecognisedPayloadIsStatedUnknownNeverGuessed — AC-1's refusal one
// level down: a source we cannot name is named UnknownSource, not attributed
// to whichever tool happens to be configured.
func TestAnUnrecognisedPayloadIsStatedUnknownNeverGuessed(t *testing.T) {
	for _, body := range []string{
		`{"resourceLogs":[{"resource":{"attributes":[{"key":"service.name","value":{"stringValue":"some-editor"}}]}}]}`,
		`{"resourceLogs":[{"resource":{"attributes":[]}}]}`,
		`{"resourceLogs":[]}`,
		`not json at all`,
		``,
	} {
		if got := SourceOf([]byte(body)); got != UnknownSource {
			t.Errorf("SourceOf(%.40q) = %q, want %q", body, got, UnknownSource)
		}
	}
}

// TestPerSourceRecordIsBesideTheTelemetryRecordNotInsideIt — the existing
// telemetry.json is read by doctor's machine-wide and per-session checks; a
// second writer inside it is how one of them loses the other's keys.
func TestPerSourceRecordIsBesideTheTelemetryRecordNotInsideIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	if SourcesPath() == StatePath() {
		t.Fatal("the per-source record shares a file with the telemetry record")
	}
	if filepath.Dir(SourcesPath()) != filepath.Dir(StatePath()) {
		t.Fatalf("SourcesPath %q is not beside StatePath %q", SourcesPath(), StatePath())
	}
}

// TestSourcesOnDiskIsEmptyNotAnErrorWhenNothingHasForwarded — the same refusal
// SessionsOnDisk makes: "not tracked yet" is never "nothing is arriving".
func TestSourcesOnDiskIsEmptyNotAnErrorWhenNothingHasForwarded(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	if got := SourcesOnDisk(); len(got) != 0 {
		t.Fatalf("got %+v, want empty", got)
	}
	if at, ok := LastForwardForSource("codex"); ok || !at.IsZero() {
		t.Fatalf("LastForwardForSource on an empty record = (%v, %v), want (zero, false)", at, ok)
	}
}

// TestAForwardRecordsThePerSourceInstant is the whole point of the file: row 6
// of the decision table needs "did CODEX's telemetry arrive", and one global
// instant cannot answer it.
func TestAForwardRecordsThePerSourceInstant(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	body, err := os.ReadFile(filepath.Join("testdata", "otlp-codex-logs.json"))
	if err != nil {
		t.Fatal(err)
	}
	p := New(srv.URL, srv.URL, func() string { return "t" }, "s", t.TempDir())
	post(t, p, "/v1/logs", "s", string(body))
	p.WaitIdle()

	at, ok := LastForwardForSource("codex")
	if !ok || at.IsZero() {
		t.Fatalf("codex forward not recorded: (%v, %v)", at, ok)
	}
	if _, ok := LastForwardForSource("claude_code"); ok {
		t.Fatal("a codex forward was also credited to claude_code")
	}
}

// TestAFailedForwardRecordsNothing — the per-source record means "telemetry for
// this tool REACHED Atlas", exactly like LastForward. Recording an attempt
// would make a machine with no network read as working.
func TestAFailedForwardRecordsNothing(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	body, err := os.ReadFile(filepath.Join("testdata", "otlp-codex-logs.json"))
	if err != nil {
		t.Fatal(err)
	}
	p := fast(New("http://127.0.0.1:1/v1/logs", "http://127.0.0.1:1/v1/metrics",
		func() string { return "t" }, "s", t.TempDir()))
	post(t, p, "/v1/logs", "s", string(body))
	p.WaitIdle()

	if _, ok := LastForwardForSource("codex"); ok {
		t.Fatal("a forward that never reached Atlas was recorded as one")
	}
}

// TestTheRecordSurvivesADaemonRestart — New() loads it, for the reason the
// session record is loaded: an empty map written back over a real history
// erases it rather than merely not reading it.
func TestTheRecordSurvivesADaemonRestart(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	claude, err := os.ReadFile(filepath.Join("testdata", "otlp-claude-code-logs.json"))
	if err != nil {
		t.Fatal(err)
	}
	codex, err := os.ReadFile(filepath.Join("testdata", "otlp-codex-logs.json"))
	if err != nil {
		t.Fatal(err)
	}

	p1 := New(srv.URL, srv.URL, func() string { return "t" }, "s", t.TempDir())
	post(t, p1, "/v1/logs", "s", string(claude))
	p1.WaitIdle()

	// A "restart": a second Proxy over the same KELD_HOME.
	p2 := New(srv.URL, srv.URL, func() string { return "t" }, "s", t.TempDir())
	post(t, p2, "/v1/logs", "s", string(codex))
	p2.WaitIdle()

	got := SourcesOnDisk()
	if len(got) != 2 {
		t.Fatalf("record holds %+v, want both claude_code and codex", got)
	}
}

// TestTheRecordIsBounded — a hostile or novel payload must not grow the file
// without limit. Everything unrecognised shares one key.
func TestTheRecordIsBounded(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	r := newSourceRecord()
	now := time.Now()
	for i := 0; i < 200; i++ {
		r.note(now, UnknownSource)
	}
	if len(SourcesOnDisk()) != 1 {
		t.Fatalf("unknown sources did not share one key: %+v", SourcesOnDisk())
	}
}
