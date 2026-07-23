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
	// Isolate PATH so the shadowing check sees no keld (the dev machine's real
	// PATH may have several) — this test is about the hook-config problem only.
	t.Setenv("PATH", t.TempDir())

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

// TestStatusReportsReauthRequired verifies that `keld signal status` surfaces
// the daemon's local "re-authentication required" marker when present.
func TestStatusReportsReauthRequired(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	if err := os.WriteFile(paths.ReauthMarkerPath(), []byte("re-authentication required (401) — run 'keld login' then 'keld-agent restart'\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	manifest := &config.Manifest{Tools: map[string]config.ToolManifest{}}
	if err := manifest.Save(); err != nil {
		t.Fatalf("saving manifest: %v", err)
	}

	var buf bytes.Buffer
	orig := console.Out
	console.Out = &buf
	defer func() { console.Out = orig }()

	cmd := newStatusCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("status should not error; got %v", err)
	}

	out := buf.String()
	if !bytes.Contains([]byte(out), []byte("re-authentication required")) {
		t.Errorf("expected output to mention re-authentication required; got: %s", out)
	}
}

// TestStatusNoReauthLineWithoutMarker verifies the re-auth line is absent
// when the marker file doesn't exist.
func TestStatusNoReauthLineWithoutMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	manifest := &config.Manifest{Tools: map[string]config.ToolManifest{}}
	if err := manifest.Save(); err != nil {
		t.Fatalf("saving manifest: %v", err)
	}

	var buf bytes.Buffer
	orig := console.Out
	console.Out = &buf
	defer func() { console.Out = orig }()

	cmd := newStatusCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("status should not error; got %v", err)
	}

	out := buf.String()
	if bytes.Contains([]byte(out), []byte("re-authentication required")) {
		t.Errorf("expected no re-auth message; got: %s", out)
	}
}

// TestDoctorReportsReauthRequired verifies that `keld signal doctor` flags the
// re-authentication-required marker as a problem.
func TestDoctorReportsReauthRequired(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	if err := os.WriteFile(paths.ReauthMarkerPath(), []byte("re-authentication required (401) — run 'keld login' then 'keld-agent restart'\n"), 0o600); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	manifest := &config.Manifest{Tools: map[string]config.ToolManifest{}}
	if err := manifest.Save(); err != nil {
		t.Fatalf("saving manifest: %v", err)
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
	if !bytes.Contains([]byte(out), []byte("re-authentication required")) {
		t.Errorf("expected output to mention re-authentication required; got: %s", out)
	}
}

// TestDoctorOkWithoutReauthMarker verifies doctor reports a healthy
// authenticated line (and no error) when the re-auth marker is absent and
// there are no other problems.
func TestDoctorOkWithoutReauthMarker(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	// Isolate PATH so the shadowing check doesn't fire on the dev machine's PATH.
	t.Setenv("PATH", t.TempDir())

	manifest := &config.Manifest{Tools: map[string]config.ToolManifest{}}
	if err := manifest.Save(); err != nil {
		t.Fatalf("saving manifest: %v", err)
	}

	var buf bytes.Buffer
	orig := console.Out
	console.Out = &buf
	defer func() { console.Out = orig }()

	cmd := newDoctorCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("doctor should return nil (no problems); got %v", err)
	}

	out := buf.String()
	if bytes.Contains([]byte(out), []byte("re-authentication required")) {
		t.Errorf("expected no re-auth message; got: %s", out)
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

func TestKeldPATHBinariesDetectsShadowing(t *testing.T) {
	writeExec := func(dir string) {
		if err := os.WriteFile(filepath.Join(dir, "keld"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	sep := string(os.PathListSeparator)

	d1 := t.TempDir()
	d2 := t.TempDir()
	writeExec(d1)
	writeExec(d2)

	// Two distinct keld binaries on PATH → shadowing (2 detected, PATH order).
	t.Setenv("PATH", d1+sep+d2)
	got := keldPATHBinaries()
	if len(got) != 2 {
		t.Fatalf("expected 2 shadowing keld, got %d: %v", len(got), got)
	}
	if got[0] != filepath.Join(d1, "keld") {
		t.Fatalf("PATH order not preserved: winner=%q", got[0])
	}

	// Single dir → exactly one, no shadowing.
	t.Setenv("PATH", d1)
	if got := keldPATHBinaries(); len(got) != 1 {
		t.Fatalf("expected 1 keld, got %d: %v", len(got), got)
	}

	// A second PATH entry that is a symlink to the first's keld → deduped to 1
	// (same underlying binary, not a real shadow).
	d3 := t.TempDir()
	if err := os.Symlink(filepath.Join(d1, "keld"), filepath.Join(d3, "keld")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", d1+sep+d3)
	if got := keldPATHBinaries(); len(got) != 1 {
		t.Fatalf("expected 1 keld (symlink deduped), got %d: %v", len(got), got)
	}
}

func TestDoctorReportsPATHShadowing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)

	// Clean config so the ONLY problem doctor can find is PATH shadowing:
	// hook.json present (no hook problem), no tools (no drift), no reauth marker.
	hookPath := paths.HookConfigPath()
	if err := os.MkdirAll(filepath.Dir(hookPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(hookPath, []byte(`{"endpoint":"http://e","ingest_token":"t"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := &config.Manifest{Tools: map[string]config.ToolManifest{}, Hook: &config.HookRecord{Version: "x"}}
	if err := manifest.Save(); err != nil {
		t.Fatal(err)
	}

	// Two distinct keld binaries on PATH → shadowing.
	d1 := t.TempDir()
	d2 := t.TempDir()
	for _, d := range []string{d1, d2} {
		if err := os.WriteFile(filepath.Join(d, "keld"), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", d1+string(os.PathListSeparator)+d2)

	var buf bytes.Buffer
	orig := console.Out
	console.Out = &buf
	defer func() { console.Out = orig }()

	cmd := newDoctorCmd()
	err := cmd.RunE(cmd, nil)
	if !errors.Is(err, errs.ErrSilentExit) {
		t.Fatalf("doctor should fail on PATH shadowing; got err=%v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("multiple keld binaries on PATH")) {
		t.Fatalf("doctor should report shadowing; output:\n%s", buf.String())
	}
}
