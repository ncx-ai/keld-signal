package cli

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

func runSignalEnv(t *testing.T, args ...string) (string, error, int) {
	t.Helper()
	restarts := 0
	old := restartSignalService
	restartSignalService = func() error { restarts++; return nil }
	t.Cleanup(func() { restartSignalService = old })
	cmd := newSignalEnvCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err, restarts
}

func isolateAtlasEnv(t *testing.T) {
	t.Helper()
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("KELD_API_URL", "")
	t.Setenv("KELD_ATLAS_WEB_URL", "")
	t.Setenv("KELD_CTX_ENDPOINT", "")
	t.Setenv("KELD_CTX_TOKEN", "")
	paths.SetAPIBaseOverride("")
}

func TestSignalEnvShowsProductionByDefault(t *testing.T) {
	isolateAtlasEnv(t)
	out, err, restarts := runSignalEnv(t)
	if err != nil || restarts != 0 || !strings.Contains(out, "Atlas: prod (https://atlas.keld.co)") {
		t.Fatalf("out=%q err=%v restarts=%d", out, err, restarts)
	}
}

func TestSignalEnvLocalSwitchesAndRestarts(t *testing.T) {
	isolateAtlasEnv(t)
	out, err, restarts := runSignalEnv(t, "local")
	if err != nil || restarts != 1 {
		t.Fatalf("err=%v restarts=%d out=%q", err, restarts, out)
	}
	if !strings.Contains(out, "Atlas: local (API http://localhost:8000, web http://localhost:3000)") || !strings.Contains(out, "Signal restarted.") {
		t.Fatalf("out=%q", out)
	}
	if paths.APIBase() != "http://localhost:8000" || paths.AtlasWebBase() != "http://localhost:3000" {
		t.Fatalf("APIBase=%q web=%q", paths.APIBase(), paths.AtlasWebBase())
	}
}

func TestSignalEnvNoRestartOnlySaves(t *testing.T) {
	isolateAtlasEnv(t)
	out, err, restarts := runSignalEnv(t, "dev", "--no-restart")
	if err != nil || restarts != 0 || !strings.Contains(out, "keld signal restart") {
		t.Fatalf("out=%q err=%v restarts=%d", out, err, restarts)
	}
	if paths.APIBase() != "https://atlas-dev.keld.co" {
		t.Fatalf("APIBase=%q", paths.APIBase())
	}
}

func TestSignalEnvRefusesAnUnknownName(t *testing.T) {
	isolateAtlasEnv(t)
	_, err, restarts := runSignalEnv(t, "staging")
	if err == nil || restarts != 0 || !strings.Contains(err.Error(), "unknown Atlas environment") {
		t.Fatalf("err=%v restarts=%d", err, restarts)
	}
}

func TestSignalEnvSaysAPairingStays(t *testing.T) {
	isolateAtlasEnv(t)
	if err := os.WriteFile(paths.HookConfigPath(), []byte(`{"endpoint":"http://localhost:8000","ingest_token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _, _ := runSignalEnv(t, "dev")
	if !strings.Contains(out, "Still paired, and sending to http://localhost:8000") {
		t.Fatalf("out=%q", out)
	}
}

func TestSignalEnvSaysWhenTheRestartFailed(t *testing.T) {
	isolateAtlasEnv(t)
	old := restartSignalService
	restartSignalService = func() error { return errors.New("no service installed") }
	t.Cleanup(func() { restartSignalService = old })
	cmd := newSignalEnvCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetArgs([]string{"dev"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "Signal did not restart (no service installed)") {
		t.Fatalf("out=%q", buf.String())
	}
}

func TestAtlasEnvNoteOnlyOffProduction(t *testing.T) {
	isolateAtlasEnv(t)
	if n := atlasEnvNote(); n != "" {
		t.Fatalf("production must say nothing, got %q", n)
	}
	if _, _, _ = runSignalEnv(t, "dev", "--no-restart"); atlasEnvNote() != "Atlas: dev (https://atlas-dev.keld.co), not production. `keld signal env prod` switches back." {
		t.Fatalf("dev note = %q", atlasEnvNote())
	}
	if _, _, _ = runSignalEnv(t, "prod", "--no-restart"); atlasEnvNote() != "" {
		t.Fatalf("back on prod the note must go: %q", atlasEnvNote())
	}
}
