package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/errs"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// TestCollectStatusReadsRealConfigForUnmanagedTool verifies FIX B: a tool whose
// config file EXISTS and is configured but is NOT recorded in the manifest is
// reported as "configured" (because collectStatus reads the real file), not
// "not installed".
func TestCollectStatusReadsRealConfigForUnmanagedTool(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "tool.json")
	if err := os.WriteFile(cfgPath, []byte(`{"configured":true}`), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	adapter := &fakeAdapter{
		name:       "faketool",
		configPath: cfgPath,
		// Status reflects the real config: configured iff the file was read.
		statusFn: func(current *string, _ map[string]any) tools.ToolStatus {
			if current != nil {
				return tools.ToolStatus{Name: "faketool", Installed: true, Configured: true}
			}
			return tools.ToolStatus{Name: "faketool", Installed: false, Configured: false}
		},
	}

	// Empty manifest — the tool is NOT recorded.
	manifest := &config.Manifest{Tools: map[string]config.ToolManifest{}}

	rows := collectStatus([]tools.Adapter{adapter}, manifest)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if !rows[0].status.Configured {
		t.Errorf("expected configured=true (config file read despite not being in manifest); got %+v", rows[0].status)
	}
}

// TestDoctorReportsMissingHookConfig verifies that doctor reports a problem when
// the manifest records a hook (Hook != nil) but hook.json does not exist on disk.
func TestDoctorReportsMissingHookConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	// Save a manifest with Hook set but without writing hook.json.
	manifest := &config.Manifest{
		Tools: map[string]config.ToolManifest{},
		Hook:  &config.HookRecord{Version: "x"},
	}
	if err := manifest.Save(); err != nil {
		t.Fatalf("saving manifest: %v", err)
	}

	// Confirm hook.json is absent.
	if _, err := os.Stat(paths.HookConfigPath()); !os.IsNotExist(err) {
		t.Fatalf("hook.json should not exist yet; err=%v", err)
	}

	var buf bytes.Buffer
	orig := console.Out
	console.Out = &buf
	defer func() { console.Out = orig }()

	cmd := newDoctorCmd()
	err := cmd.RunE(cmd, nil)
	if !errors.Is(err, errs.ErrSilentExit) {
		t.Fatalf("doctor should return ErrSilentExit; got %v", err)
	}

	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("hook.json")) && !bytes.Contains([]byte(out), []byte("hook config")) {
		t.Errorf("expected output to mention missing hook config; got: %s", out)
	}
}

// TestDoctorNoHookProblemWhenHookJsonExists verifies that doctor does NOT report
// a hook problem when hook.json is present on disk.
func TestDoctorNoHookProblemWhenHookJsonExists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	// Write hook.json so it exists.
	hookPath := paths.HookConfigPath()
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(hookPath, []byte(`{"endpoint":"http://e","ingest_token":"t"}`), 0o644); err != nil {
		t.Fatalf("write hook.json: %v", err)
	}

	manifest := &config.Manifest{
		Tools: map[string]config.ToolManifest{},
		Hook:  &config.HookRecord{Version: "x"},
	}
	if err := manifest.Save(); err != nil {
		t.Fatalf("saving manifest: %v", err)
	}

	var buf bytes.Buffer
	orig := console.Out
	console.Out = &buf
	defer func() { console.Out = orig }()

	cmd := newDoctorCmd()
	err := cmd.RunE(cmd, nil)
	if err != nil {
		t.Fatalf("doctor should return nil (no problems); got %v", err)
	}

	out := buf.String()
	if bytes.Contains([]byte(out), []byte("hook")) {
		t.Errorf("expected no hook problem message; got: %s", out)
	}
}

func TestDoctorReportsDrift(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())

	// Build a manifest that records a tool entry but whose config file doesn't
	// exist (simulating drift: manifest says configured, reality says otherwise).
	manifest := &config.Manifest{
		Tools: map[string]config.ToolManifest{
			"claude_code": {
				Name:       "claude_code",
				ConfigPath: "/nonexistent/path/settings.json",
				Managed:    map[string]any{},
			},
		},
	}
	if err := manifest.Save(); err != nil {
		t.Fatalf("saving manifest: %v", err)
	}

	// Capture console output.
	var buf bytes.Buffer
	orig := console.Out
	console.Out = &buf
	defer func() { console.Out = orig }()

	// The real ClaudeAdapter.Status will return not-installed/not-configured
	// when the config file is absent, which satisfies the drift condition.
	adapter, err := tools.Get("claude_code")
	if err != nil {
		t.Fatalf("get adapter: %v", err)
	}
	st := adapter.Status(nil, map[string]any{})
	if st.Configured {
		t.Skip("ClaudeAdapter reports configured with nil config — skip drift test")
	}

	cmd := newDoctorCmd()
	err = cmd.RunE(cmd, nil)
	if err == nil {
		t.Error("doctor should return an error when problems are found")
	}
	if !errors.Is(err, errs.ErrSilentExit) {
		t.Errorf("doctor should return ErrSilentExit so Execute() does not double-print; got %v", err)
	}

	out := buf.String()
	if out == "" {
		t.Error("doctor should print problem output")
	}
	// The output should contain a drift message for Claude Code.
	if !bytes.Contains([]byte(out), []byte("claude")) && !bytes.Contains([]byte(out), []byte("Claude")) {
		t.Errorf("expected drift message mentioning Claude Code; got: %s", out)
	}
}
