package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

	// This test is about the hook-config problem only. Without an
	// agent-config.json, ml_backend defaults to "auto", which needs GLiNER2 —
	// absent in this fresh KELD_HOME, and doctor would (correctly) also flag
	// that. Pin ml_backend to "deterministic" so the model check stays silent
	// and doesn't interfere with what this test asserts.
	if err := os.WriteFile(paths.AgentConfigPath(), []byte(`{"ml_backend":"deterministic"}`), 0o600); err != nil {
		t.Fatalf("write agent-config.json: %v", err)
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

	// See TestDoctorNoHookProblemWhenHookJsonExists: pin ml_backend so the new
	// model-state check (this test predates it) stays silent.
	if err := os.WriteFile(paths.AgentConfigPath(), []byte(`{"ml_backend":"deterministic"}`), 0o600); err != nil {
		t.Fatalf("write agent-config.json: %v", err)
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
	if err := os.WriteFile(paths.AgentConfigPath(), []byte(`{"ml_backend":"deterministic"}`), 0o600); err != nil {
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

// baseDoctorFixture writes the config that keeps every OTHER doctor check
// quiet (hook.json present, empty manifest, no reauth marker, isolated PATH),
// so a test can isolate the on-device-model check.
func baseDoctorFixture(t *testing.T, agentConfigJSON string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	t.Setenv("PATH", t.TempDir())
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
	if agentConfigJSON != "" {
		if err := os.WriteFile(paths.AgentConfigPath(), []byte(agentConfigJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDoctorModelState_NotNeededStaysSilent covers the design's central
// constraint: an absent model that the current configuration doesn't need
// must never become a problem line, for each way a model can be "not needed".
func TestDoctorModelState_NotNeededStaysSilent(t *testing.T) {
	cases := []struct {
		name       string
		agentCfg   string
		envTextEmb string
	}{
		{"ml_backend deterministic: GLiNER2 not needed", `{"ml_backend":"deterministic"}`, ""},
		{"ml_backend off: GLiNER2 not needed", `{"ml_backend":"off"}`, ""},
		{"KELD_TEXTEMBED unset: encoder not needed even with features on", `{"ml_backend":"deterministic","features":true}`, ""},
		{"features toggle off: encoder not needed even with KELD_TEXTEMBED=1", `{"ml_backend":"deterministic","features":false}`, "1"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			baseDoctorFixture(t, c.agentCfg)
			if c.envTextEmb != "" {
				t.Setenv("KELD_TEXTEMBED", c.envTextEmb)
			}

			var buf bytes.Buffer
			orig := console.Out
			console.Out = &buf
			defer func() { console.Out = orig }()

			cmd := newDoctorCmd()
			if err := cmd.RunE(cmd, nil); err != nil {
				t.Fatalf("doctor should report no problems (not-needed absent model must stay silent); got err=%v, output:\n%s", err, buf.String())
			}
			out := buf.String()
			if bytes.Contains([]byte(out), []byte("GLiNER2")) || bytes.Contains([]byte(out), []byte("text encoder")) || bytes.Contains([]byte(out), []byte("text-encoder")) {
				t.Fatalf("expected no model mention when not needed; got:\n%s", out)
			}
		})
	}
}

// TestDoctorModelState_NeededAndAbsentIsAProblem is the flip side: a model
// the current configuration DOES need, and whose weights are confirmed
// absent, must produce a problem line naming the reason, without changing
// doctor's non-model behavior otherwise.
func TestDoctorModelState_NeededAndAbsentIsAProblem(t *testing.T) {
	t.Run("GLiNER2 under auto (default)", func(t *testing.T) {
		baseDoctorFixture(t, "") // no agent-config.json -> ml_backend defaults to auto
		var buf bytes.Buffer
		orig := console.Out
		console.Out = &buf
		defer func() { console.Out = orig }()

		cmd := newDoctorCmd()
		err := cmd.RunE(cmd, nil)
		if !errors.Is(err, errs.ErrSilentExit) {
			t.Fatalf("doctor should report the missing-and-needed GLiNER2 model as a problem; err=%v", err)
		}
		if !bytes.Contains(buf.Bytes(), []byte("GLiNER2")) {
			t.Fatalf("expected a GLiNER2 problem line; got:\n%s", buf.String())
		}
		if !bytes.Contains(buf.Bytes(), []byte("ml_backend")) {
			t.Fatalf("expected the reason (ml_backend) stated; got:\n%s", buf.String())
		}
	})

	t.Run("text encoder with both toggles on", func(t *testing.T) {
		baseDoctorFixture(t, `{"ml_backend":"deterministic","features":true}`)
		t.Setenv("KELD_TEXTEMBED", "1")
		var buf bytes.Buffer
		orig := console.Out
		console.Out = &buf
		defer func() { console.Out = orig }()

		cmd := newDoctorCmd()
		err := cmd.RunE(cmd, nil)
		if !errors.Is(err, errs.ErrSilentExit) {
			t.Fatalf("doctor should report the missing-and-needed encoder as a problem; err=%v", err)
		}
		if !bytes.Contains(buf.Bytes(), []byte("text encoder")) {
			t.Fatalf("expected a text-encoder problem line; got:\n%s", buf.String())
		}
		if !bytes.Contains(buf.Bytes(), []byte("KELD_TEXTEMBED")) {
			t.Fatalf("expected the reason (KELD_TEXTEMBED) stated; got:\n%s", buf.String())
		}
	})
}

// TestDoctorModelState_PresentIsNeverAProblem confirms that when the weights
// ARE on disk, doctor stays quiet regardless of need.
func TestDoctorModelState_PresentIsNeverAProblem(t *testing.T) {
	baseDoctorFixture(t, "") // ml_backend defaults to auto -> GLiNER2 needed
	dir := paths.ModelsDir("gliner2-large-v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "model.safetensors"), []byte("not empty"), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	orig := console.Out
	console.Out = &buf
	defer func() { console.Out = orig }()

	cmd := newDoctorCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("doctor should report no problems when the needed model is present; got err=%v, output:\n%s", err, buf.String())
	}
	if bytes.Contains(buf.Bytes(), []byte("GLiNER2")) {
		t.Fatalf("expected no GLiNER2 mention when its weights are present; got:\n%s", buf.String())
	}
}

// TestStatusModelState_ShowsWhatItCanDetermine exercises the informational
// surface (`keld signal status`): a needed-and-absent model gets a line, and
// an absent-and-not-needed model produces no on-device-models section at all.
func TestStatusModelState_ShowsWhatItCanDetermine(t *testing.T) {
	t.Run("needed and absent is shown", func(t *testing.T) {
		baseDoctorFixture(t, "") // auto -> GLiNER2 needed, absent in this fresh KELD_HOME
		var buf bytes.Buffer
		orig := console.Out
		console.Out = &buf
		defer func() { console.Out = orig }()

		cmd := newStatusCmd()
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("status should not error; got %v", err)
		}
		out := buf.String()
		if !bytes.Contains([]byte(out), []byte("On-device models:")) {
			t.Fatalf("expected an on-device-models section; got:\n%s", out)
		}
		if !bytes.Contains([]byte(out), []byte("gliner2")) {
			t.Fatalf("expected a gliner2 line; got:\n%s", out)
		}
	})

	t.Run("not needed and absent stays out of the section entirely", func(t *testing.T) {
		baseDoctorFixture(t, `{"ml_backend":"deterministic"}`)
		var buf bytes.Buffer
		orig := console.Out
		console.Out = &buf
		defer func() { console.Out = orig }()

		cmd := newStatusCmd()
		if err := cmd.RunE(cmd, nil); err != nil {
			t.Fatalf("status should not error; got %v", err)
		}
		out := buf.String()
		if bytes.Contains([]byte(out), []byte("On-device models:")) {
			t.Fatalf("expected no on-device-models section when nothing is needed or present; got:\n%s", out)
		}
	})
}

// doctor is what a user runs to answer "is my install actually working?", but it
// only ever checked CONFIG. On a real machine it printed "No problems found."
// while the spool held five abandoned legacy writes, the oldest a month old —
// i.e. while enrichment had not delivered anything for weeks. Config validity is
// not health, and reporting clean through a stuck spool is the failure mode that
// makes a broken install look fine.
func TestDoctorReportsStaleSpoolBacklog(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	// Pin ml_backend so this test isolates the SPOOL check. Without it the backend
	// defaults to "auto", whose model check correctly reports GLiNER2 absent — a real
	// finding, but not the one this test is about.
	if err := os.WriteFile(paths.AgentConfigPath(), []byte(`{"ml_backend":"deterministic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&config.Manifest{Tools: map[string]config.ToolManifest{}}).Save(); err != nil {
		t.Fatalf("saving manifest: %v", err)
	}
	spoolDir := filepath.Join(home, "spool")
	if err := os.MkdirAll(spoolDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"a.json.tmp", "b.json.tmp"} {
		if err := os.WriteFile(filepath.Join(spoolDir, n), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var buf bytes.Buffer
	orig := console.Out
	console.Out = &buf
	defer func() { console.Out = orig }()

	cmd := newDoctorCmd()
	err := cmd.RunE(cmd, nil)
	if !errors.Is(err, errs.ErrSilentExit) {
		t.Fatalf("doctor must FAIL on a stale spool backlog, got %v", err)
	}
	if out := buf.String(); !strings.Contains(out, "spool") {
		t.Errorf("expected the spool backlog to be named in the output; got: %s", out)
	}
}

// The clean case must stay clean: an empty spool directory is not a problem, and
// neither is a spool directory that does not exist yet.
func TestDoctorSilentOnHealthySpool(t *testing.T) {
	home := t.TempDir()
	t.Setenv("KELD_HOME", home)
	// Pin ml_backend so this test isolates the SPOOL check. Without it the backend
	// defaults to "auto", whose model check correctly reports GLiNER2 absent — a real
	// finding, but not the one this test is about.
	if err := os.WriteFile(paths.AgentConfigPath(), []byte(`{"ml_backend":"deterministic"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (&config.Manifest{Tools: map[string]config.ToolManifest{}}).Save(); err != nil {
		t.Fatalf("saving manifest: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "spool"), 0o700); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	orig := console.Out
	console.Out = &buf
	defer func() { console.Out = orig }()

	cmd := newDoctorCmd()
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("doctor should pass on an empty spool; got %v", err)
	}
}
