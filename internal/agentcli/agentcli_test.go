package agentcli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

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
		func() (string, error) { return "/fake/keld", nil }, run, func() error { installed = true; return nil })
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
		func() (string, error) { return "/fake/keld", nil }, run, func() error { installed = true; return nil })
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
		func() (string, error) { return "/fake/keld", nil }, run, func() error { installed = true; return nil })
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
		func() (string, error) { return "/fake/keld", nil }, run, func() error { installed = true; return nil })
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
		func() (string, error) { return "/fake/keld", nil }, run, func() error { return nil })
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
		func() (string, error) { return "/fake/keld", nil }, run, func() error { return nil })
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
		func() (string, error) { return "/fake/keld", nil }, run, func() error { return nil })
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
		func() (string, error) { return "/fake/keld", nil }, run, func() error { return nil })
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

	if err := runInstall(installConfig{}, func() bool { return true }, resolve, run, install); err == nil {
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

	if err := runInstall(installConfig{}, func() bool { return false }, resolve, run, install); err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("no-TTY must not run login/setup, got %v", calls)
	}
	if !installed {
		t.Fatal("service install must still run in no-TTY mode")
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
