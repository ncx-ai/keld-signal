package cli

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/tools"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// unreachableTelemetryEndpoint is a loopback port nothing binds, so the
// post-write probe reaches the "no daemon running" branch.
//
// ⚠️ NEVER the real 14318. These tests run on developer machines where a daemon
// IS listening, with its own secret — the probe would then 401 against a live
// proxy and the test would roll back the tool config it had just written. A test
// that behaves differently depending on what is running on the machine is a test
// that mutates it.
const unreachableTelemetryEndpoint = "http://127.0.0.1:1"

// seedToolForSetup builds a detected tool with an existing config file, so the
// write path produces a real pristine backup to roll back to.
func seedToolForSetup(t *testing.T, before string) (*fakeAdapter, string) {
	t.Helper()
	cfgPath := filepath.Join(t.TempDir(), "tool.json")
	if err := os.WriteFile(cfgPath, []byte(before), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	return &fakeAdapter{
		name:       "faketool",
		configPath: cfgPath,
		plan: tools.Plan{
			Name:       "faketool",
			ConfigPath: cfgPath,
			AfterText:  `{"keld":"configured"}`,
			Managed:    map[string]any{},
			Changed:    true,
		},
	}, cfgPath
}

// ⚠️ THE 2026-09-18 INCIDENT IS EXACTLY THIS CHECK FAILING FOR WANT OF EXISTING.
// A keld 3.0.0-rc.3 still on PATH wrote secret a5629e92… into ~/.codex/config.toml
// and ~/.claude/settings.json while the running proxy held 26908e20…. Setup
// reported success. A probe POST with the token it had just written returned 401
// — one request, and the machine would have been known broken instead of
// discovered days later with Codex's telemetry dead.
//
// Restoring is part of the refusal, not a nicety: leaving the wrong secret in the
// file is leaving the machine in the broken state the probe just proved.
func TestSetupRefusesAndRestoresWhenTheProxyRejectsTheSecretItJustWrote(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	const before = `{"existing":"config"}`
	adapter, cfgPath := seedToolForSetup(t, before)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	var out bytes.Buffer
	oldOut := console.Out
	console.Out = &out
	defer func() { console.Out = oldOut }()

	ob := &api.Onboarding{Endpoint: "https://atlas.keld.co", IngestToken: "prod-token", Actor: "dg@keld.co"}
	p := tools.SetupParams{Endpoint: srv.URL, IngestToken: "the-secret-just-written"}
	opts := SetupOpts{Yes: true, Confirm: func(string) bool { return true },
		ResolveConflict: func(tools.Adapter, tools.Plan) string { return "skip" }}

	if _, err := runSetup([]tools.Adapter{adapter}, p, &api.Client{}, ob, opts); err == nil {
		t.Fatal("setup succeeded against a proxy that 401s the secret it wrote")
	}

	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != before {
		t.Fatalf("tool config not restored:\n got  %s\n want %s", got, before)
	}
	if !bytes.Contains(out.Bytes(), []byte("401")) {
		t.Errorf("the refusal did not say what was found:\n%s", out.String())
	}
	// ⚠️ And it must not print the credential it was testing. The whole point of
	// the local secret is that it stays local; a terminal, a CI log and an
	// installer transcript are all places it would then live.
	if bytes.Contains(out.Bytes(), []byte("the-secret-just-written")) {
		t.Error("the refusal printed the secret value")
	}
}

// ⚠️ NO DAEMON IS NOT A FAILURE. `keld-agent install` runs setup BEFORE it
// registers and starts the service, and the macOS pkg onboards before the agent
// exists at all — so the ordinary first install has nothing listening on the
// loopback port. Treating that as a broken machine would fail every fresh
// install on the one path that has to work.
func TestSetupContinuesWhenNoDaemonIsListening(t *testing.T) {
	t.Setenv("KELD_HOME", t.TempDir())
	adapter, cfgPath := seedToolForSetup(t, `{"existing":"config"}`)

	var out bytes.Buffer
	oldOut := console.Out
	console.Out = &out
	defer func() { console.Out = oldOut }()

	ob := &api.Onboarding{Endpoint: "https://atlas.keld.co", IngestToken: "prod-token", Actor: "dg@keld.co"}
	p := tools.SetupParams{Endpoint: unreachableTelemetryEndpoint, IngestToken: "local-secret"}
	opts := SetupOpts{Yes: true, Confirm: func(string) bool { return true },
		ResolveConflict: func(tools.Adapter, tools.Plan) string { return "skip" }}

	if _, err := runSetup([]tools.Adapter{adapter}, p, &api.Client{}, ob, opts); err != nil {
		t.Fatalf("setup failed with no daemon running: %v", err)
	}
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"keld":"configured"}` {
		t.Fatalf("tool config was rolled back over an unverifiable probe: %s", got)
	}
	if !bytes.Contains(out.Bytes(), []byte("could not verify")) {
		t.Errorf("setup did not say the check was inconclusive:\n%s", out.String())
	}
}

// writeFakeKeld puts an executable `keld` in its own directory that reports ver.
func writeFakeKeld(t *testing.T, ver string) string {
	t.Helper()
	dir := t.TempDir()
	script := "#!/bin/sh\necho \"keld version " + ver + "\"\n"
	if err := os.WriteFile(filepath.Join(dir, "keld"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ⚠️ THIS IS THE INCIDENT'S ROOT CAUSE, NOT ITS SYMPTOM. The secret on the
// maintainer's machine was written by a keld 3.0.0-rc.3 at /usr/local/keld/keld,
// ahead of the current install on PATH: a binary that predates the secret's own
// file, so it minted into agent.json and disagreed with the running proxy. A
// newer binary knows things this one does not, and the only safe move is to
// refuse and name it.
func TestVersionGuardRefusesANewerKeldOnPATH(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stubs are not executable as `keld` on Windows")
	}
	newerDir := writeFakeKeld(t, "3.1.0")
	t.Setenv("PATH", newerDir)

	path, ver := newerKeldOnPATH("3.0.0")
	if path == "" {
		t.Fatal("a newer keld on PATH was not detected")
	}
	if ver != "3.1.0" {
		t.Fatalf("reported version %q, want 3.1.0", ver)
	}
	if filepath.Dir(path) != newerDir {
		t.Fatalf("named %q, want the binary in %s", path, newerDir)
	}
}

func TestVersionGuardIsSilentForOlderEqualOrUnknown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stubs are not executable as `keld` on Windows")
	}
	for _, c := range []struct{ onPath, running string }{
		{"3.0.0", "3.0.1"},       // older
		{"3.0.0", "3.0.0"},       // equal
		{"3.0.0-rc.3", "3.0.0"},  // a pre-release does not outrank its release
		{"3.0.0", "dev"},         // a source build cannot tell, so it must not accuse
		{"garbage", "3.0.0"},     // unreadable is unknown, never "newer"
		{"3.0.0", "not-a-thing"}, // …in either direction
	} {
		t.Setenv("PATH", writeFakeKeld(t, c.onPath))
		if path, ver := newerKeldOnPATH(c.running); path != "" {
			t.Errorf("running %s, on PATH %s: refused over %q (%s)", c.running, c.onPath, path, ver)
		}
	}
}

// The guard has to bite in the command, not only in its helper: a setup that
// detects the shadow and writes anyway has detected nothing.
func TestSetupRefusesToWriteWhenANewerKeldShadowsIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stubs are not executable as `keld` on Windows")
	}
	t.Setenv("KELD_HOME", t.TempDir())
	t.Setenv("PATH", writeFakeKeld(t, "9.9.9"))
	prev := version.CLI
	version.CLI = "3.0.0"
	defer func() { version.CLI = prev }()

	const before = `{"existing":"config"}`
	adapter, cfgPath := seedToolForSetup(t, before)

	var out bytes.Buffer
	oldOut := console.Out
	console.Out = &out
	defer func() { console.Out = oldOut }()

	ob := &api.Onboarding{Endpoint: "https://atlas.keld.co", IngestToken: "prod-token", Actor: "dg@keld.co"}
	p := tools.SetupParams{Endpoint: unreachableTelemetryEndpoint, IngestToken: "local-secret"}
	opts := SetupOpts{Yes: true, Confirm: func(string) bool { return true },
		ResolveConflict: func(tools.Adapter, tools.Plan) string { return "skip" }}

	if _, err := runSetup([]tools.Adapter{adapter}, p, &api.Client{}, ob, opts); err == nil {
		t.Fatal("setup wrote tool configs while a newer keld shadowed it")
	}
	got, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != before {
		t.Fatalf("the refusal still wrote the tool config: %s", got)
	}
	if _, err := os.Stat(paths.HookConfigPath()); err == nil {
		t.Error("the refusal adopted the onboarding anyway")
	}
	if !bytes.Contains(out.Bytes(), []byte("9.9.9")) {
		t.Errorf("the refusal did not name the newer binary's version:\n%s", out.String())
	}
}
