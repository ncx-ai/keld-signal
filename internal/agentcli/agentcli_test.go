package agentcli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/console"
)

func TestKeldInDir(t *testing.T) {
	dir := t.TempDir()

	if _, ok := keldInDir(dir); ok {
		t.Fatal("expected keld not found in empty dir")
	}

	bin := filepath.Join(dir, keldName())
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, ok := keldInDir(dir)
	if !ok {
		t.Fatal("expected keld found after creating it")
	}
	if got != bin {
		t.Fatalf("got %q, want %q", got, bin)
	}

	if runtime.GOOS == "windows" && keldName() != "keld.exe" {
		t.Fatalf("windows keldName = %q, want keld.exe", keldName())
	}
}

// helper: records "name arg arg" per call
func recorder() (*[]string, stepRunner) {
	var calls []string
	return &calls, func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
}

func TestRunInstallSequence(t *testing.T) { // no code, TTY → login, signal setup (no --yes)
	calls, run := recorder()
	installed := false
	err := runInstall(installConfig{}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, noopConfig, func() error { installed = true; return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{"/fake/keld login", "/fake/keld signal setup"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("steps = %v, want %v", *calls, want)
	}
	if !installed {
		t.Fatal("service install was not called")
	}
}

func TestRunInstallWithCodeIsHeadlessCapable(t *testing.T) { // code set, no TTY → still onboards
	calls, run := recorder()
	installed := false
	err := runInstall(installConfig{code: "AB12-CD34"}, func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, noopConfig, func() error { installed = true; return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{"/fake/keld login --code AB12-CD34", "/fake/keld signal setup --yes"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("steps = %v, want %v", *calls, want)
	}
	if !installed {
		t.Fatal("service install must run after code onboarding")
	}
}

func TestRunInstallCodeAbortsBeforeService(t *testing.T) {
	var calls []string
	run := func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		if strings.Contains(calls[len(calls)-1], "login") {
			return errors.New("bad code")
		}
		return nil
	}
	installed := false
	err := runInstall(installConfig{code: "NOPE"}, func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, noopConfig, func() error { installed = true; return nil })
	if err == nil {
		t.Fatal("expected error when login --code fails")
	}
	if len(calls) != 1 || installed {
		t.Fatalf("must stop after login; calls=%v installed=%v", calls, installed)
	}
}

func TestRunInstallTTYLoginFailureAbortsBeforeService(t *testing.T) {
	var calls []string
	run := func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		if strings.Contains(calls[len(calls)-1], "login") {
			return errors.New("login boom")
		}
		return nil
	}
	installed := false
	err := runInstall(installConfig{}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, noopConfig, func() error { installed = true; return nil })
	if err == nil {
		t.Fatal("expected error when login fails in the TTY branch")
	}
	if len(calls) != 1 || installed {
		t.Fatalf("must stop after login; calls=%v installed=%v", calls, installed)
	}
}

func TestRunInstallApiURLAndJSONPassthrough(t *testing.T) {
	calls, run := recorder()
	err := runInstall(installConfig{code: "X1-Y2", apiURL: "http://localhost:8000", jsonOut: true},
		func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, noopConfig, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{
		"/fake/keld login --api-url http://localhost:8000 --json --code X1-Y2",
		"/fake/keld signal setup --api-url http://localhost:8000 --json --yes",
	}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("steps = %v, want %v", *calls, want)
	}
}

func TestRunInstallYesInTTY(t *testing.T) { // no code, TTY, yes=true → setup --yes
	calls, run := recorder()
	err := runInstall(installConfig{yes: true}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, noopConfig, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{"/fake/keld login", "/fake/keld signal setup --yes"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("steps = %v, want %v", *calls, want)
	}
}

// runInstall prints a "Starting the agent…" header before installService()
// and a "✓ keld-agent running" confirmation on success, in human mode.
func TestRunInstallPrintsStartingAgentHeaderHuman(t *testing.T) {
	_, run := recorder()

	var buf bytes.Buffer
	old := console.Out
	console.Out = &buf
	defer func() { console.Out = old }()

	err := runInstall(installConfig{}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, noopConfig, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, "Starting the agent…") {
		t.Fatalf("missing 'Starting the agent…' header: %q", got)
	}
	if !strings.Contains(got, "✓ keld-agent running") {
		t.Fatalf("missing '✓ keld-agent running' confirmation: %q", got)
	}
}

// The --json passthrough mode must stay a clean NDJSON stream from login/setup
// subprocesses — runInstall itself must not inject any human console lines.
func TestRunInstallSuppressesHumanLinesInJSONMode(t *testing.T) {
	_, run := recorder()

	var buf bytes.Buffer
	old := console.Out
	console.Out = &buf
	defer func() { console.Out = old }()

	err := runInstall(installConfig{jsonOut: true, code: "X1"}, func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, noopConfig, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if buf.String() != "" {
		t.Fatalf("jsonOut must suppress human lines from runInstall itself, got %q", buf.String())
	}
}

func TestRunInstallAbortsWhenKeldMissing(t *testing.T) {
	resolve := func() (string, error) { return "", errors.New("not found") }
	ran := false
	run := func(name string, args ...string) error { ran = true; return nil }
	installed := false
	install := func() error { installed = true; return nil }

	if err := runInstall(installConfig{}, func() bool { return true }, resolve, run, noopConfig, install); err == nil {
		t.Fatal("expected error when keld is missing")
	}
	if ran || installed {
		t.Fatal("no steps should run when keld cannot be resolved")
	}
}

func TestRunInstallNoTTYSkipsLoginAndSetup(t *testing.T) {
	var calls []string
	resolve := func() (string, error) { return "/fake/keld", nil }
	run := func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	installed := false
	install := func() error { installed = true; return nil }

	if err := runInstall(installConfig{}, func() bool { return false }, resolve, run, noopConfig, install); err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("no-TTY must not run login/setup, got %v", calls)
	}
	if !installed {
		t.Fatal("service install must still run in no-TTY mode")
	}
}

// TestRunInstallHeadlessBeatsATrueTTY is the regression guard for the Windows
// installer hang. Inno Setup's `runhidden` hides the WINDOW but leaves the child
// with a real console, so stdout IS a terminal there and stdoutIsTTY answers true
// — the isTTY seam below stands in for that. Without --headless the install then
// ran `keld login` in an invisible window and `keld signal setup` blocked forever
// on its [Y/n] prompt, wedging the installer until the process was killed by hand.
// --headless must therefore win over isTTY, not merely agree with it.
func TestRunInstallHeadlessBeatsATrueTTY(t *testing.T) {
	var calls []string
	resolve := func() (string, error) { return "/fake/keld", nil }
	run := func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	installed := false
	install := func() error { installed = true; return nil }

	err := runInstall(installConfig{headless: true}, func() bool { return true },
		resolve, run, noopConfig, install)
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("--headless must run no interactive step even on a TTY, got %v", calls)
	}
	if !installed {
		t.Fatal("--headless must still register the service")
	}
}

// A setup code carries its own non-interactive login, so --headless must not
// suppress it: onboard.cmd re-runs `keld-agent install --code`, and a machine
// pushed by MDM is finished the same way. Headless means "do not PROMPT", never
// "do not onboard".
func TestRunInstallHeadlessStillRedeemsACode(t *testing.T) {
	calls, run := recorder()
	installed := false
	err := runInstall(installConfig{headless: true, code: "AB12-CD34"}, func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, noopConfig,
		func() error { installed = true; return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{"/fake/keld login --code AB12-CD34", "/fake/keld signal setup --yes"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("steps = %v, want %v", *calls, want)
	}
	if !installed {
		t.Fatal("service install must run after code onboarding")
	}
}

// The flag has to be REACHABLE from the command line, or the .iss passes an
// unknown flag and `keld-agent install --headless` fails outright — a worse
// failure than the hang it replaces, and one no runInstall-level test can see.
func TestInstallCmdAcceptsHeadlessFlag(t *testing.T) {
	root := NewRootCmd()
	var install *cobra.Command
	for _, c := range root.Commands() {
		if c.Name() == "install" {
			install = c
		}
	}
	if install == nil {
		t.Fatal("no install command")
	}
	if install.Flags().Lookup("headless") == nil {
		t.Fatal("install has no --headless flag; installers/windows/keld-agent.iss passes it")
	}
}

// printStatus is the testable core of `keld-agent status`: statusFn/reauthFn
// are seams standing in for service.Status (OS-specific, not easily driven in
// a unit test) and paths.ReauthRequired.
func TestPrintStatusWithReauthMarker(t *testing.T) {
	var buf bytes.Buffer
	old := console.Out
	console.Out = &buf
	defer func() { console.Out = old }()

	statusFn := func() (string, error) { return "active", nil }
	reauthFn := func() (bool, string) { return true, "re-authentication required (401)\n2026-07-14T00:00:00Z\n" }

	// printStatus uses fmt.Println (stdlib stdout), not console.Print, so
	// capture os.Stdout instead of console.Out.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	if err := printStatus(statusFn, reauthFn); err != nil {
		t.Fatalf("printStatus: %v", err)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	os.Stdout = origStdout

	if !strings.Contains(string(out), "active") {
		t.Errorf("expected status output; got: %s", out)
	}
	if !strings.Contains(string(out), "re-authentication required") {
		t.Errorf("expected re-auth line; got: %s", out)
	}
}

func TestPrintStatusWithoutReauthMarker(t *testing.T) {
	statusFn := func() (string, error) { return "active", nil }
	reauthFn := func() (bool, string) { return false, "" }

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = origStdout }()

	if err := printStatus(statusFn, reauthFn); err != nil {
		t.Fatalf("printStatus: %v", err)
	}
	w.Close()
	out, _ := io.ReadAll(r)
	os.Stdout = origStdout

	if strings.Contains(string(out), "re-authentication required") {
		t.Errorf("expected no re-auth line; got: %s", out)
	}
}

func TestPrintStatusPropagatesStatusFnError(t *testing.T) {
	wantErr := errors.New("service not installed")
	statusFn := func() (string, error) { return "", wantErr }
	called := false
	reauthFn := func() (bool, string) { called = true; return false, "" }

	if err := printStatus(statusFn, reauthFn); !errors.Is(err, wantErr) {
		t.Fatalf("expected statusFn error to propagate; got %v", err)
	}
	if called {
		t.Error("reauthFn should not be called when statusFn errors")
	}
}

func TestServiceControlCommandsRegistered(t *testing.T) {
	root := NewRootCmd()
	have := map[string]bool{}
	for _, c := range root.Commands() {
		have[c.Name()] = true
	}
	for _, v := range []string{"start", "stop", "restart"} {
		if !have[v] {
			t.Errorf("keld-agent missing %q command", v)
		}
	}
}

func TestExecuteCmdPrintsErrorToStderr(t *testing.T) {
	root := &cobra.Command{
		Use:           "x",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE:          func(*cobra.Command, []string) error { return errors.New("boom") },
	}
	var buf bytes.Buffer
	code := executeCmd(root, &buf)
	if code != 1 {
		t.Fatalf("code = %d, want 1", code)
	}
	if !strings.Contains(buf.String(), "boom") {
		t.Fatalf("stderr = %q, want to contain \"boom\"", buf.String())
	}
}

func TestExecuteCmdSuccessIsSilent(t *testing.T) {
	root := &cobra.Command{Use: "x", RunE: func(*cobra.Command, []string) error { return nil }}
	var buf bytes.Buffer
	code := executeCmd(root, &buf)
	if code != 0 {
		t.Fatalf("code = %d, want 0", code)
	}
	if buf.String() != "" {
		t.Fatalf("stderr = %q, want empty", buf.String())
	}
}

// noopConfig is the config writer for tests that are not about the config.
func noopConfig(string, bool) error { return nil }

// configRecorder records what runInstall asked to be written.
type configRecorder struct {
	calls   int
	backend string
	blocks  bool
	err     error
}

func (c *configRecorder) write(backend string, blocks bool) error {
	c.calls++
	c.backend = backend
	c.blocks = blocks
	return c.err
}

func TestRunInstallWritesV2Config(t *testing.T) {
	_, run := recorder()
	var cfgw configRecorder
	err := runInstall(installConfig{backend: "deterministic"}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, cfgw.write, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if cfgw.calls != 1 {
		t.Fatalf("writeConfig called %d times, want 1", cfgw.calls)
	}
	if cfgw.backend != "deterministic" || !cfgw.blocks {
		t.Errorf("wrote backend=%q blocks=%v, want deterministic/true", cfgw.backend, cfgw.blocks)
	}
}

// The headless branch registers the service without onboarding — it still needs
// the right config, or a GUI-installer machine runs v1 behaviour forever.
func TestRunInstallWritesConfigInHeadlessBranch(t *testing.T) {
	_, run := recorder()
	var cfgw configRecorder
	err := runInstall(installConfig{backend: "deterministic"}, func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, cfgw.write, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if cfgw.calls != 1 {
		t.Errorf("writeConfig called %d times in the headless branch, want 1", cfgw.calls)
	}
}

// ml_backend is startup-only, so the write is worthless unless the service
// restart that follows it actually happens after it. Pin the ORDER.
func TestRunInstallWritesConfigBeforeInstallingService(t *testing.T) {
	_, run := recorder()
	var order []string
	writeConfig := func(string, bool) error { order = append(order, "config"); return nil }
	installService := func() error { order = append(order, "service"); return nil }

	err := runInstall(installConfig{backend: "deterministic"}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, writeConfig, installService)
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{"config", "service"}
	if strings.Join(order, ",") != strings.Join(want, ",") {
		t.Errorf("order = %v, want %v — ml_backend is read at startup, so the "+
			"restart in installService must come after the write", order, want)
	}
}

// A config write that fails must abort before anything is registered or started.
func TestRunInstallConfigFailureAbortsBeforeService(t *testing.T) {
	_, run := recorder()
	cfgw := configRecorder{err: errors.New("disk full")}
	installed := false
	err := runInstall(installConfig{backend: "deterministic"}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, cfgw.write,
		func() error { installed = true; return nil })
	if err == nil {
		t.Fatal("want an error when the config write fails, got nil")
	}
	if installed {
		t.Error("service was installed despite a failed config write")
	}
}

func TestRunInstallHonoursBackendOverride(t *testing.T) {
	_, run := recorder()
	var cfgw configRecorder
	err := runInstall(installConfig{backend: "auto"}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, cfgw.write, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if cfgw.backend != "auto" {
		t.Errorf("backend = %q, want auto — --backend must be able to put a "+
			"machine back on the model without hand-editing JSON", cfgw.backend)
	}
}

// An empty backend (nothing passed) must default to deterministic, not to the
// empty string — which Settings reads as "auto" and would silently make every
// install a v1 install.
func TestRunInstallDefaultsToDeterministic(t *testing.T) {
	_, run := recorder()
	var cfgw configRecorder
	err := runInstall(installConfig{}, func() bool { return true },
		func() (string, error) { return "/fake/keld", nil }, run, cfgw.write, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if cfgw.backend != "deterministic" {
		t.Errorf("backend = %q with none given, want deterministic", cfgw.backend)
	}
}

// A pairing code must set --api-url on BOTH child commands. If only login learned
// the host, `signal setup` would re-resolve on its own and write the previous
// endpoint into hook.json — a split-brain install that looks like it worked.
func TestRunInstallPairingCodeSetsAPIURLOnBothSteps(t *testing.T) {
	calls, run := recorder()
	err := runInstall(installConfig{code: "atlas.keld.co/abcd-efgh"}, func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, noopConfig, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{
		"/fake/keld login --api-url https://atlas.keld.co --code ABCD-EFGH",
		"/fake/keld signal setup --api-url https://atlas.keld.co --yes",
	}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("steps = %v, want %v", *calls, want)
	}
}

// An explicit --api-url outranks the host carried by the code.
func TestRunInstallFlagBeatsPairingCodeHost(t *testing.T) {
	calls, run := recorder()
	err := runInstall(installConfig{code: "atlas.keld.co/ABCD-EFGH", apiURL: "http://localhost:8000"},
		func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, noopConfig, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	for _, c := range *calls {
		if !strings.Contains(c, "--api-url http://localhost:8000") {
			t.Fatalf("explicit flag should win in every step, got %v", *calls)
		}
	}
}

// A bare code still works exactly as before — no host, no --api-url.
func TestRunInstallBareCodeUnchanged(t *testing.T) {
	calls, run := recorder()
	err := runInstall(installConfig{code: "ABCD-EFGH"}, func() bool { return false },
		func() (string, error) { return "/fake/keld", nil }, run, noopConfig, func() error { return nil })
	if err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	want := []string{"/fake/keld login --code ABCD-EFGH", "/fake/keld signal setup --yes"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("steps = %v, want %v", *calls, want)
	}
}
