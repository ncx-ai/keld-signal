package cli

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/agent/daemon"
	"github.com/ncx-ai/keld-signal/internal/agent/ingress"
	"github.com/ncx-ai/keld-signal/internal/agent/integrations"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/localagent"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// fixtureMachine builds a HOME that produces more than one state: Codex
// configured but with a session older than its config (restart_required),
// Claude Code's directory present and unconfigured, and the unsupported rows
// absent. Everything is written through the real adapters, so the fixture is a
// machine rather than a hand-built Response.
func fixtureMachine(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("KELD_HOME", filepath.Join(home, ".keld"))
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))

	// A rollout whose session began well before anything was configured. Its
	// modtime is left alone; what matters is the record's own timestamp.
	sessions := filepath.Join(home, ".codex", "sessions", "2026", "09", "01")
	if err := os.MkdirAll(sessions, 0o755); err != nil {
		t.Fatal(err)
	}
	rollout := `{"timestamp":"2026-09-01T09:00:00Z","type":"session_meta","payload":{"id":"abc","cwd":"/tmp","cli_version":"0.153.4"}}`
	if err := os.WriteFile(filepath.Join(sessions, "rollout-abc.jsonl"), []byte(rollout+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".codex", "config.toml"), []byte("model = \"gpt-5\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Claude Code installed, never configured.
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Configure Codex through the same call the detector and the setup route
	// make — not by writing a manifest by hand.
	e, ok := integrations.Get("codex")
	if !ok {
		t.Fatal("catalogue has no codex")
	}
	m, err := config.LoadManifest()
	if err != nil {
		t.Fatal(err)
	}
	params := func() (tools.SetupParams, error) {
		return tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "local-secret", BinPath: "/usr/local/bin/keld"}, nil
	}
	if _, err := integrations.ApplyEntry(e, tools.Get, params, m); err != nil {
		t.Fatal(err)
	}
	return home
}

// routeStates asks the real loopback route, behind the real secret middleware.
func routeStates(t *testing.T) integrations.Response {
	t.Helper()
	mux := http.NewServeMux()
	daemon.IntegrationsRoute(nil, nil)(mux, ingress.RequireSecret("s3cret"))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/v1/integrations", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-keld-agent-secret", "s3cret")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body integrations.Response
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	return body
}

func captureConsole(t *testing.T, fn func()) string {
	t.Helper()
	var buf bytes.Buffer
	orig := console.Out
	console.Out = &buf
	defer func() { console.Out = orig }()
	fn()
	return buf.String()
}

// AC-8: doctor and status print the states the ROUTE serves, for the same
// machine, because both come from integrations.Compute through one function.
// A second copy of the rule would show up here as a disagreement.
func TestDoctorIntegrationsParity(t *testing.T) {
	fixtureMachine(t)

	route := routeStates(t)
	if len(route.Integrations) == 0 {
		t.Fatal("the route answered no integrations")
	}

	// The fixture must actually produce a state worth disagreeing about.
	var codex integrations.Integration
	for _, in := range route.Integrations {
		if in.ID == "codex" {
			codex = in
		}
	}
	if codex.State != integrations.RestartRequired {
		t.Fatalf("fixture produced codex=%q, want %q — the parity check needs a non-trivial state",
			codex.State, integrations.RestartRequired)
	}
	if codex.ToolVersion != "0.153.4" {
		t.Fatalf("codex tool_version = %q, want 0.153.4 from session_meta.cli_version", codex.ToolVersion)
	}

	statusOut := captureConsole(t, func() {
		cmd := newStatusCmd()
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("status: %v", err)
		}
	})

	// Every row the route published appears in status with the SAME state
	// string, verbatim.
	for _, in := range route.Integrations {
		want := in.DisplayName + " "
		var found bool
		for _, line := range strings.Split(statusOut, "\n") {
			if strings.Contains(line, in.DisplayName) && strings.Contains(line, string(in.State)) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("status never printed %q with state %q (looking for %q):\n%s",
				in.DisplayName, in.State, want, statusOut)
		}
	}

	doctorOut := captureConsole(t, func() {
		cmd := newDoctorCmd()
		_ = cmd.RunE(cmd, nil) // problems ⇒ ErrSilentExit; the output is what matters
	})

	// Doctor names the actionable row and quotes the shared sentence.
	if !strings.Contains(doctorOut, "Codex") || !strings.Contains(doctorOut, string(integrations.RestartRequired)) {
		t.Fatalf("doctor did not report codex's %q:\n%s", integrations.RestartRequired, doctorOut)
	}
	if !strings.Contains(doctorOut, integrations.InstructionRestart) {
		t.Fatalf("doctor did not print the shared instruction sentence:\n%s", doctorOut)
	}

	// And it does NOT report the quiet ones. A tool that is not on this
	// machine is not a finding, and neither is an unsupported catalogue row.
	for _, in := range route.Integrations {
		if in.State != integrations.NotInstalled && in.State != integrations.Unsupported {
			continue
		}
		for _, line := range strings.Split(doctorOut, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "✗") && strings.Contains(line, in.DisplayName) {
				t.Fatalf("doctor reported %q (%s) as a problem:\n%s", in.DisplayName, in.State, doctorOut)
			}
		}
	}
}

// The CLI holds NO switch over states of its own: it formats what Compute
// produced. A value outside the vocabulary must print verbatim rather than be
// mapped to something the CLI thinks it recognises — the same rule the pane
// follows.
func TestCLIPrintsAnUnknownStateVerbatimRatherThanMappingIt(t *testing.T) {
	fixtureMachine(t)
	lines := localagent.IntegrationLines(integrations.Response{
		Integrations: []integrations.Integration{{ID: "x", DisplayName: "Mystery", State: "some_future_state"}},
	})
	if len(lines) != 1 || !strings.Contains(lines[0], "some_future_state") {
		t.Fatalf("lines = %v, want the server's string verbatim", lines)
	}
}
