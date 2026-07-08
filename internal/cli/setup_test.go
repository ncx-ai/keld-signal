package cli

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/errs"
	"github.com/ncx-ai/keld-signal/internal/tools"
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
	p := tools.SetupParams{Endpoint: ob.Endpoint, IngestToken: ob.IngestToken, Actor: ob.Actor}
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

	adapter := &fakeAdapter{
		name: "faketool",
		plan: tools.Plan{
			Name:       "faketool",
			ConfigPath: cfgPath,
			AfterText:  `{"key":"val"}`,
			Managed:    map[string]any{},
			Summary:    []string{"added key"},
			Changed:    true,
		},
	}

	ob := &api.Onboarding{Endpoint: "https://ep.example.com", IngestToken: "tok", Actor: "actor1"}
	client := &api.Client{}
	p := tools.SetupParams{Endpoint: ob.Endpoint, IngestToken: ob.IngestToken, Actor: ob.Actor}

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
	if m == nil {
		t.Error("expected non-nil manifest")
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
	p := tools.SetupParams{Endpoint: ob.Endpoint, IngestToken: ob.IngestToken, Actor: ob.Actor}

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
	p := tools.SetupParams{Endpoint: ob.Endpoint, IngestToken: ob.IngestToken, Actor: ob.Actor}

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
	p := tools.SetupParams{Endpoint: ob.Endpoint, IngestToken: ob.IngestToken, Actor: ob.Actor}

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
	p := tools.SetupParams{Endpoint: ob.Endpoint, IngestToken: ob.IngestToken, Actor: ob.Actor}

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
