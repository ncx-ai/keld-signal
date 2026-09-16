package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"encoding/json"

	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/errs"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/tools"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// fakeAdapter is a test double for tools.Adapter.
type fakeAdapter struct {
	name       string
	configPath string
	plan       tools.Plan
	removePlan tools.Plan
	status     tools.ToolStatus
	// statusFn, when set, computes the status from the current config text and
	// managed map (used to verify that callers read the real config file).
	statusFn func(current *string, managed map[string]any) tools.ToolStatus
}

func (f *fakeAdapter) Name() string        { return f.name }
func (f *fakeAdapter) DisplayName() string { return f.name }
func (f *fakeAdapter) Detect() bool        { return true }
func (f *fakeAdapter) ConfigPath() string {
	if f.configPath != "" {
		return f.configPath
	}
	return f.plan.ConfigPath
}
func (f *fakeAdapter) Apply(_ *string, _ tools.SetupParams, _ bool) tools.Plan {
	return f.plan
}
func (f *fakeAdapter) Remove(_ *string, _ map[string]any) tools.Plan { return f.removePlan }
func (f *fakeAdapter) Status(current *string, managed map[string]any) tools.ToolStatus {
	if f.statusFn != nil {
		return f.statusFn(current, managed)
	}
	return f.status
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// readHookConfig reads ~/.keld/hook.json the way the hook and the daemon do.
// config keeps its file struct unexported, so the test asserts on the BYTES on
// disk rather than on a loader it would then be trusting.
func readHookConfig(t *testing.T) struct {
	Endpoint    string `json:"endpoint"`
	IngestToken string `json:"ingest_token"`
} {
	t.Helper()
	var hf struct {
		Endpoint    string `json:"endpoint"`
		IngestToken string `json:"ingest_token"`
	}
	b, err := os.ReadFile(paths.HookConfigPath())
	if err != nil {
		t.Fatalf("reading hook.json: %v", err)
	}
	if err := json.Unmarshal(b, &hf); err != nil {
		t.Fatalf("parsing hook.json: %v", err)
	}
	return hf
}

// ⚠️ AN UPGRADE MUST ADOPT THE CREDENTIAL IT JUST VERIFIED, EVEN WHEN NO TOOL
// CONFIG CHANGES. `SaveHookConfig` and the manifest write used to sit BELOW the
// `len(approveds) == 0` early return, so a machine whose tools were already
// configured discarded a verified onboarding and kept whatever hook.json it had.
//
// Measured on a real install (2026-09-16, v3.0.0 from the release): the wizard
// signed in to production, `postinstall` ran `signal setup --yes`, every tool
// reported "already configured", and hook.json kept YESTERDAY's endpoint —
// `http://localhost:8000` with a dev token. The agent log then showed 882 calls
// to localhost against 9 to atlas.keld.co, while `keld signal status` reported
// the production login. `doctor` said no problems, correctly: nothing compared
// the two.
//
// The installer is supposed to replace and restart everything. A pass that
// leaves the daemon reporting somewhere other than the identity it just
// verified is an internally inconsistent machine, which is worse than a loud
// failure.
func TestRunSetupWritesHookConfigWhenEveryToolIsAlreadyConfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	// The state an upgrade finds: a hook.json pointing somewhere else entirely.
	if err := config.SaveHookConfig("http://localhost:8000", "stale-dev-token"); err != nil {
		t.Fatalf("seeding hook.json: %v", err)
	}

	// Nothing to apply: the one detected tool is already configured, which is
	// the ordinary state of every re-install and every upgrade.
	adapter := &fakeAdapter{
		name: "faketool",
		plan: tools.Plan{
			Name:       "faketool",
			ConfigPath: filepath.Join(t.TempDir(), "tool.json"),
			AfterText:  `{"key":"val"}`,
			Managed:    map[string]any{},
			Changed:    false,
		},
	}

	ob := &api.Onboarding{Endpoint: "https://atlas.keld.co", IngestToken: "fresh-prod-token", Actor: "dg@keld.co"}
	p := tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "local-secret"}
	opts := SetupOpts{
		Yes:             true,
		Confirm:         func(string) bool { return true },
		ResolveConflict: func(tools.Adapter, tools.Plan) string { return "skip" },
	}

	if _, err := runSetup([]tools.Adapter{adapter}, p, &api.Client{}, ob, opts); err != nil {
		t.Fatalf("runSetup returned error: %v", err)
	}

	hook := readHookConfig(t)
	if hook.Endpoint != ob.Endpoint {
		t.Errorf("hook.json endpoint = %q, want %q — the verified onboarding was discarded", hook.Endpoint, ob.Endpoint)
	}
	if hook.IngestToken != ob.IngestToken {
		t.Error("hook.json kept the stale ingest token instead of the one setup just obtained")
	}

	// The manifest records which CLI wrote the hook; leaving it behind is what
	// made `keld signal status` report a stale hook version after an upgrade.
	m, err := config.LoadManifest()
	if err != nil {
		t.Fatalf("loading manifest: %v", err)
	}
	if m == nil || m.Hook == nil || m.Hook.Version != version.CLI {
		got := "<nil>"
		if m != nil && m.Hook != nil {
			got = m.Hook.Version
		}
		t.Errorf("manifest hook version = %s, want %s", got, version.CLI)
	}
}

// The companion refusal: a dry run inspects, it does not adopt. Without this,
// the fix above would let `--dry-run` rewrite the machine's credential — which
// is exactly what the wizard pane runs before anyone has agreed to anything.
func TestRunSetupDryRunDoesNotAdoptTheOnboarding(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	if err := config.SaveHookConfig("http://localhost:8000", "stale-dev-token"); err != nil {
		t.Fatalf("seeding hook.json: %v", err)
	}

	adapter := &fakeAdapter{
		name: "faketool",
		plan: tools.Plan{
			Name:       "faketool",
			ConfigPath: filepath.Join(t.TempDir(), "tool.json"),
			AfterText:  `{"key":"val"}`,
			Managed:    map[string]any{},
			Changed:    true,
		},
	}

	ob := &api.Onboarding{Endpoint: "https://atlas.keld.co", IngestToken: "fresh-prod-token", Actor: "dg@keld.co"}
	p := tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "local-secret"}
	opts := SetupOpts{
		DryRun:          true,
		Yes:             true,
		Confirm:         func(string) bool { return true },
		ResolveConflict: func(tools.Adapter, tools.Plan) string { return "skip" },
	}

	if _, err := runSetup([]tools.Adapter{adapter}, p, &api.Client{}, ob, opts); err != nil {
		t.Fatalf("runSetup returned error: %v", err)
	}

	hook := readHookConfig(t)
	if hook.Endpoint != "http://localhost:8000" || hook.IngestToken != "stale-dev-token" {
		t.Errorf("dry run rewrote hook.json: endpoint=%q", hook.Endpoint)
	}
}

func TestRunSetupEmitsEventsWhenEmitSet(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := t.TempDir()

	// One adapter that will be configured, one that reports no change.
	changed := &fakeAdapter{
		name: "configured_tool",
		plan: tools.Plan{
			Name: "configured_tool", ConfigPath: filepath.Join(dir, "a.json"),
			AfterText: `{"k":1}`, Managed: map[string]any{}, Summary: []string{"add"}, Changed: true,
		},
	}
	nochange := &fakeAdapter{
		name: "nochange_tool",
		plan: tools.Plan{
			Name: "nochange_tool", ConfigPath: filepath.Join(dir, "b.json"),
			AfterText: "", Managed: map[string]any{}, Changed: false,
		},
	}

	var events []SetupEvent
	ob := &api.Onboarding{Endpoint: "https://ep", IngestToken: "tok", Actor: "actor"}
	// Tools get the daemon's loopback address and the LOCAL secret — never the
	// org ingest token, which runSetup now refuses. See telemetryTarget.
	p := tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "local-secret"}
	opts := SetupOpts{Yes: true, Emit: func(e SetupEvent) { events = append(events, e) }}

	if _, err := runSetup([]tools.Adapter{changed, nochange}, p, &api.Client{}, ob, opts); err != nil {
		t.Fatalf("runSetup: %v", err)
	}

	var tool0, tool1, done *SetupEvent
	for i := range events {
		switch {
		case events[i].Kind == "tool" && events[i].Name == "configured_tool":
			tool0 = &events[i]
		case events[i].Kind == "tool" && events[i].Name == "nochange_tool":
			tool1 = &events[i]
		case events[i].Kind == "done":
			done = &events[i]
		}
	}
	if tool0 == nil || tool0.Action != "configured" {
		t.Fatalf("configured_tool event = %+v", tool0)
	}
	if tool1 == nil || tool1.Action != "already_configured" {
		t.Fatalf("nochange_tool event = %+v", tool1)
	}
	if done == nil || done.Configured != 1 {
		t.Fatalf("done event = %+v", done)
	}
}

func TestRunSetupDryRunWritesNothing(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "tool.json")
	// Simulates GeminiAdapter's second artifact (~/.gemini/.env): a plan can
	// stage an ExtraFile, but --dry-run must never let it reach disk.
	extraPath := filepath.Join(dir, ".env")

	adapter := &fakeAdapter{
		name: "faketool",
		plan: tools.Plan{
			Name:       "faketool",
			ConfigPath: cfgPath,
			AfterText:  `{"key":"val"}`,
			Managed:    map[string]any{},
			Summary:    []string{"added key"},
			Changed:    true,
			ExtraFile:  &tools.ExtraFile{Path: extraPath, AfterText: "OTEL_EXPORTER_OTLP_HEADERS=x-keld-ingest-token=tok,x-keld-actor=me\n", Mode: 0o600},
		},
	}

	ob := &api.Onboarding{Endpoint: "https://ep.example.com", IngestToken: "tok", Actor: "actor1"}
	client := &api.Client{}
	// Tools get the daemon's loopback address and the LOCAL secret — never the
	// org ingest token, which runSetup now refuses. See telemetryTarget.
	p := tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "local-secret"}

	opts := SetupOpts{
		DryRun:          true,
		Yes:             true,
		Confirm:         func(string) bool { return true },
		ResolveConflict: func(tools.Adapter, tools.Plan) string { return "skip" },
	}

	m, err := runSetup([]tools.Adapter{adapter}, p, client, ob, opts)
	if err != nil {
		t.Fatalf("runSetup returned error: %v", err)
	}

	if fileExists(cfgPath) {
		t.Error("dry-run: config file should not have been created")
	}
	if fileExists(extraPath) {
		t.Error("dry-run: gemini-style ExtraFile (.env) should not have been created")
	}
	if m == nil {
		t.Error("expected non-nil manifest")
	}
}

// TestRunSetupDryRunEmitsWillConfigureForApprovedTool pins the fix for the
// macOS wizard pane's empty tool checklist: a --dry-run run only ever emitted
// `tool` events for skipped_conflict/already_configured, never for a detected,
// unconflicted, changed adapter — the common case on a fresh Mac with Claude
// Code installed and unconfigured. The pane renders exactly the `tool` events
// runSetup emits, so that gap rendered "No supported AI tools found on this
// Mac." for a tool sitting right there. A dry run must say what it WOULD do.
func TestRunSetupDryRunEmitsWillConfigureForApprovedTool(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := t.TempDir()

	adapter := &fakeAdapter{
		name: "faketool",
		plan: tools.Plan{
			Name:       "faketool",
			ConfigPath: filepath.Join(dir, "tool.json"),
			AfterText:  `{"key":"val"}`,
			Managed:    map[string]any{},
			Summary:    []string{"added key"},
			Changed:    true,
		},
	}

	ob := &api.Onboarding{Endpoint: "https://ep.example.com", IngestToken: "tok", Actor: "actor1"}
	client := &api.Client{}
	p := tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "local-secret"}

	var events []SetupEvent
	opts := SetupOpts{
		DryRun:  true,
		Yes:     true,
		Confirm: func(string) bool { return true },
		Emit:    func(e SetupEvent) { events = append(events, e) },
	}

	if _, err := runSetup([]tools.Adapter{adapter}, p, client, ob, opts); err != nil {
		t.Fatalf("runSetup returned error: %v", err)
	}

	var tool *SetupEvent
	for i := range events {
		if events[i].Kind == "tool" && events[i].Name == "faketool" {
			tool = &events[i]
		}
	}
	if tool == nil {
		t.Fatalf("dry run emitted no tool event for an approved, unconflicted, changed adapter; events=%+v", events)
	}
	if tool.Action != "will_configure" {
		t.Fatalf("expected action %q, got %q", "will_configure", tool.Action)
	}
}

// TestRunSetupConfirmedApplyWritesExtraFile covers the write-on-confirm path:
// once the user confirms (or --yes is set) and dry-run is off, a plan's
// ExtraFile must be written to disk at the given mode alongside the primary
// config file.
func TestRunSetupConfirmedApplyWritesExtraFile(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "tool.json")
	extraPath := filepath.Join(dir, ".env")

	adapter := &fakeAdapter{
		name: "faketool",
		plan: tools.Plan{
			Name:       "faketool",
			ConfigPath: cfgPath,
			AfterText:  `{"key":"val"}`,
			Managed:    map[string]any{"created": true},
			Summary:    []string{"added key"},
			Changed:    true,
			ExtraFile:  &tools.ExtraFile{Path: extraPath, AfterText: "OTEL_EXPORTER_OTLP_HEADERS=x-keld-ingest-token=tok,x-keld-actor=me\n", Mode: 0o600},
		},
	}

	ob := &api.Onboarding{Endpoint: "https://ep.example.com", IngestToken: "tok", Actor: "actor1"}
	client := &api.Client{}
	// Tools get the daemon's loopback address and the LOCAL secret — never the
	// org ingest token, which runSetup now refuses. See telemetryTarget.
	p := tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "local-secret"}

	opts := SetupOpts{
		DryRun:  false,
		Yes:     true,
		Confirm: func(string) bool { return true },
	}

	if _, err := runSetup([]tools.Adapter{adapter}, p, client, ob, opts); err != nil {
		t.Fatalf("runSetup returned error: %v", err)
	}

	if !fileExists(cfgPath) {
		t.Fatal("confirmed apply: config file should have been created")
	}
	data, err := os.ReadFile(extraPath)
	if err != nil {
		t.Fatalf("confirmed apply: ExtraFile should have been written: %v", err)
	}
	if string(data) != "OTEL_EXPORTER_OTLP_HEADERS=x-keld-ingest-token=tok,x-keld-actor=me\n" {
		t.Fatalf("ExtraFile contents = %q, want %q", data, "OTEL_EXPORTER_OTLP_HEADERS=x-keld-ingest-token=tok,x-keld-actor=me\n")
	}
	info, err := os.Stat(extraPath)
	if err != nil {
		t.Fatalf("stat ExtraFile: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("ExtraFile mode = %o, want 0600", perm)
	}
}

func TestRunSetupNormalApply(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "tool.json")

	adapter := &fakeAdapter{
		name: "faketool",
		plan: tools.Plan{
			Name:       "faketool",
			ConfigPath: cfgPath,
			AfterText:  `{"key":"val"}`,
			Managed:    map[string]any{"created": true},
			Summary:    []string{"added key"},
			Changed:    true,
		},
	}

	ob := &api.Onboarding{Endpoint: "https://ep.example.com", IngestToken: "tok", Actor: "actor1"}
	client := &api.Client{}
	// Tools get the daemon's loopback address and the LOCAL secret — never the
	// org ingest token, which runSetup now refuses. See telemetryTarget.
	p := tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "local-secret"}

	opts := SetupOpts{
		DryRun:          false,
		Yes:             true,
		Confirm:         func(string) bool { return true },
		ResolveConflict: func(tools.Adapter, tools.Plan) string { return "skip" },
	}

	m, err := runSetup([]tools.Adapter{adapter}, p, client, ob, opts)
	if err != nil {
		t.Fatalf("runSetup returned error: %v", err)
	}

	if !fileExists(cfgPath) {
		t.Error("config file should have been created")
	}
	if _, ok := m.Tools["faketool"]; !ok {
		t.Error("manifest should contain faketool entry")
	}
	if m.Endpoint == nil || *m.Endpoint != ob.Endpoint {
		t.Errorf("manifest endpoint = %v, want %s", m.Endpoint, ob.Endpoint)
	}
}

func TestRunSetupConflictSkip(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "tool.json")

	adapter := &fakeAdapter{
		name: "faketool",
		plan: tools.Plan{
			Name:       "faketool",
			ConfigPath: cfgPath,
			AfterText:  "",
			Managed:    map[string]any{},
			Changed:    true,
			Conflict:   "block already present",
		},
	}

	ob := &api.Onboarding{Endpoint: "https://ep.example.com", IngestToken: "tok", Actor: "actor1"}
	client := &api.Client{}
	// Tools get the daemon's loopback address and the LOCAL secret — never the
	// org ingest token, which runSetup now refuses. See telemetryTarget.
	p := tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "local-secret"}

	opts := SetupOpts{
		DryRun:          false,
		Yes:             false,
		Confirm:         func(string) bool { return true },
		ResolveConflict: func(tools.Adapter, tools.Plan) string { return "skip" },
	}

	m, err := runSetup([]tools.Adapter{adapter}, p, client, ob, opts)
	if err != nil {
		t.Fatalf("runSetup returned error: %v", err)
	}

	if fileExists(cfgPath) {
		t.Error("config file should not have been created when conflict is skipped")
	}
	if _, ok := m.Tools["faketool"]; ok {
		t.Error("skipped tool should not appear in manifest")
	}
}

// TestRunSetupAbortReturnsSilentExit verifies FIX A: resolving a conflict with
// "abort" returns errs.ErrSilentExit (so Execute() does not double-print) and
// writes nothing.
func TestRunSetupAbortReturnsSilentExit(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "tool.json")

	adapter := &fakeAdapter{
		name: "faketool",
		plan: tools.Plan{
			Name:       "faketool",
			ConfigPath: cfgPath,
			Managed:    map[string]any{},
			Changed:    true,
			Conflict:   "block already present",
		},
	}

	ob := &api.Onboarding{Endpoint: "https://ep.example.com", IngestToken: "tok", Actor: "actor1"}
	client := &api.Client{}
	// Tools get the daemon's loopback address and the LOCAL secret — never the
	// org ingest token, which runSetup now refuses. See telemetryTarget.
	p := tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "local-secret"}

	opts := SetupOpts{
		DryRun:          false,
		Yes:             false,
		Confirm:         func(string) bool { return true },
		ResolveConflict: func(tools.Adapter, tools.Plan) string { return "abort" },
	}

	m, err := runSetup([]tools.Adapter{adapter}, p, client, ob, opts)
	if !errors.Is(err, errs.ErrSilentExit) {
		t.Fatalf("expected errs.ErrSilentExit on abort; got %v", err)
	}
	if m != nil {
		t.Errorf("abort should return a nil manifest; got %+v", m)
	}
	if fileExists(cfgPath) {
		t.Error("abort must not write any config file")
	}
	if fileExists(filepath.Join(os.Getenv("KELD_HOME"), "manifest.json")) {
		t.Error("abort must not write the manifest")
	}
}

// TestRunSetupHumanOutputFormat locks the unified phased human console output:
// a single "Configuring your AI tools…" header, one aligned ✓/⚠ line per tool
// (no per-tool box rule), a single ✓ Hook line, and no stale
// "Nothing to apply." / "Setup complete…" summary text.
func TestRunSetupHumanOutputFormat(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := t.TempDir()

	nochange := &fakeAdapter{
		name: "codex",
		plan: tools.Plan{
			Name: "codex", ConfigPath: filepath.Join(dir, "codex.json"),
			Changed: false,
		},
	}

	var buf bytes.Buffer
	old := console.Out
	console.Out = &buf
	defer func() { console.Out = old }()

	ob := &api.Onboarding{Endpoint: "https://ep", IngestToken: "tok", Actor: "actor"}
	client := &api.Client{}
	// Tools get the daemon's loopback address and the LOCAL secret — never the
	// org ingest token, which runSetup now refuses. See telemetryTarget.
	p := tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "local-secret"}
	opts := SetupOpts{
		Yes:             true,
		Confirm:         func(string) bool { return true },
		ResolveConflict: func(tools.Adapter, tools.Plan) string { return "skip" },
	}

	if _, err := runSetup([]tools.Adapter{nochange}, p, client, ob, opts); err != nil {
		t.Fatalf("runSetup: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "Configuring your AI tools…") {
		t.Fatalf("missing header: %q", got)
	}
	if strings.Contains(got, "─") {
		t.Fatalf("box-drawing rule leaked into human output: %q", got)
	}
	if !regexp.MustCompile(`(?m)^\s*✓ codex\s+already configured\s*$`).MatchString(got) {
		t.Fatalf("missing already-configured tool line: %q", got)
	}
	// The Hook line now names the DESTINATION it wrote, not just the path. It
	// used to print unconditionally, above a return that wrote nothing, so an
	// install log showed the hook being configured on exactly the run that left
	// it stale — see TestRunSetupWritesHookConfigWhenEveryToolIsAlreadyConfigured.
	if !regexp.MustCompile(`(?m)^\s*✓ Hook\s+~/\.keld/hook\.json → \S+\s*$`).MatchString(got) {
		t.Fatalf("missing Hook line: %q", got)
	}
	if strings.Contains(got, "Nothing to apply.") {
		t.Fatalf("stale 'Nothing to apply.' text present: %q", got)
	}
	if strings.Contains(got, "Setup complete") {
		t.Fatalf("stale 'Setup complete' text present: %q", got)
	}
}

// TestRunSetupConflictHumanOutputFormat locks the unified ⚠ skipped-conflict line.
func TestRunSetupConflictHumanOutputFormat(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "tool.json")

	adapter := &fakeAdapter{
		name: "faketool",
		plan: tools.Plan{
			Name:       "faketool",
			ConfigPath: cfgPath,
			Managed:    map[string]any{},
			Changed:    true,
			Conflict:   "block already present",
		},
	}

	var buf bytes.Buffer
	old := console.Out
	console.Out = &buf
	defer func() { console.Out = old }()

	ob := &api.Onboarding{Endpoint: "https://ep.example.com", IngestToken: "tok", Actor: "actor1"}
	client := &api.Client{}
	// Tools get the daemon's loopback address and the LOCAL secret — never the
	// org ingest token, which runSetup now refuses. See telemetryTarget.
	p := tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "local-secret"}
	opts := SetupOpts{
		Yes:             true, // --yes auto-skips conflicts
		Confirm:         func(string) bool { return true },
		ResolveConflict: func(tools.Adapter, tools.Plan) string { return "replace" },
	}

	if _, err := runSetup([]tools.Adapter{adapter}, p, client, ob, opts); err != nil {
		t.Fatalf("runSetup: %v", err)
	}

	got := buf.String()
	if !regexp.MustCompile(`(?m)^\s*⚠ faketool\s+skipped \(conflict\)\s*$`).MatchString(got) {
		t.Fatalf("missing unified skipped-conflict line: %q", got)
	}
}

func TestRunSetupConflictYes(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "tool.json")

	adapter := &fakeAdapter{
		name: "faketool",
		plan: tools.Plan{
			Name:       "faketool",
			ConfigPath: cfgPath,
			AfterText:  "",
			Managed:    map[string]any{},
			Changed:    true,
			Conflict:   "block already present",
		},
	}

	ob := &api.Onboarding{Endpoint: "https://ep.example.com", IngestToken: "tok", Actor: "actor1"}
	client := &api.Client{}
	// Tools get the daemon's loopback address and the LOCAL secret — never the
	// org ingest token, which runSetup now refuses. See telemetryTarget.
	p := tools.SetupParams{Endpoint: "http://127.0.0.1:14318", IngestToken: "local-secret"}

	opts := SetupOpts{
		DryRun:          false,
		Yes:             true, // --yes auto-skips conflicts
		Confirm:         func(string) bool { return true },
		ResolveConflict: func(tools.Adapter, tools.Plan) string { return "replace" }, // should not be called
	}

	m, err := runSetup([]tools.Adapter{adapter}, p, client, ob, opts)
	if err != nil {
		t.Fatalf("runSetup returned error: %v", err)
	}

	if fileExists(cfgPath) {
		t.Error("config file should not exist; --yes skips conflicts, not resolves them")
	}
	if _, ok := m.Tools["faketool"]; ok {
		t.Error("auto-skipped tool should not appear in manifest")
	}
}

func TestSetupBinPathFlagOverridesRunningBinary(t *testing.T) {
	cmd := newSetupCmd()
	f := cmd.Flags().Lookup("bin-path")
	if f == nil {
		t.Fatal("keld signal setup must accept --bin-path: the installer pane runs a copy " +
			"of keld from inside the plugin bundle, and pinning THAT path into tool hooks " +
			"breaks every hook the moment the wizard closes")
	}
	if got := resolveSetupBinPath("/usr/local/keld/keld"); got != "/usr/local/keld/keld" {
		t.Fatalf("explicit bin path = %q, want /usr/local/keld/keld", got)
	}
	if got := resolveSetupBinPath(""); got != keldBinaryPath() {
		t.Fatalf("empty bin path = %q, want the running binary %q", got, keldBinaryPath())
	}
}
