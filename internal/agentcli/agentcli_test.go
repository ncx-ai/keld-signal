package agentcli

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

func TestRunInstallSequence(t *testing.T) {
	var calls []string
	resolve := func() (string, error) { return "/fake/keld", nil }
	run := func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		return nil
	}
	installed := false
	install := func() error { installed = true; return nil }

	if err := runInstall(func() bool { return true }, resolve, run, install); err != nil {
		t.Fatalf("runInstall: %v", err)
	}

	want := []string{"/fake/keld login", "/fake/keld signal setup"}
	if len(calls) != len(want) || calls[0] != want[0] || calls[1] != want[1] {
		t.Fatalf("steps = %v, want %v", calls, want)
	}
	if !installed {
		t.Fatal("service install was not called")
	}
}

func TestRunInstallAbortsOnLoginFailure(t *testing.T) {
	var calls []string
	resolve := func() (string, error) { return "/fake/keld", nil }
	run := func(name string, args ...string) error {
		calls = append(calls, strings.Join(append([]string{name}, args...), " "))
		if strings.HasSuffix(calls[len(calls)-1], "login") {
			return errors.New("boom")
		}
		return nil
	}
	installed := false
	install := func() error { installed = true; return nil }

	if err := runInstall(func() bool { return true }, resolve, run, install); err == nil {
		t.Fatal("expected error when login fails")
	}
	if len(calls) != 1 {
		t.Fatalf("expected to stop after login, got calls %v", calls)
	}
	if installed {
		t.Fatal("service install must not run when login fails")
	}
}

func TestRunInstallAbortsWhenKeldMissing(t *testing.T) {
	resolve := func() (string, error) { return "", errors.New("not found") }
	ran := false
	run := func(name string, args ...string) error { ran = true; return nil }
	installed := false
	install := func() error { installed = true; return nil }

	if err := runInstall(func() bool { return true }, resolve, run, install); err == nil {
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

	if err := runInstall(func() bool { return false }, resolve, run, install); err != nil {
		t.Fatalf("runInstall: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("no-TTY must not run login/setup, got %v", calls)
	}
	if !installed {
		t.Fatal("service install must still run in no-TTY mode")
	}
}
