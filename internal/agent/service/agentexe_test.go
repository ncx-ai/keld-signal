package service

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// THE STORY: restarting Signal from either binary leaves the service pointing
// at the daemon.
//
// ⚠️ **THIS IS THE REGRESSION TEST FOR A DEFECT THAT KILLED COLLECTION ON A
// REAL MACHINE.** `Start` and `Restart` re-derived the service definition from
// os.Executable() before kickstarting. `keld signal restart` runs inside the
// `keld` CLI, so the LaunchAgent was rewritten to `/…/keld run` — and `keld`
// has no `run` command, so the daemon died on every launch and launchd
// respawned it into a permanent failure loop. Observed: the plist read
// `<string>…/keld</string><string>run</string>` and the log was
// `unknown command "run" for "keld"`, repeating. The two commands are
// documented as equivalent, so the safe one and the destructive one were
// indistinguishable from either help text.
func TestARestartFromTheCLIKeepsTheInstalledDaemonProgram(t *testing.T) {
	dir := t.TempDir()
	installed := filepath.Join(dir, agentFileName())
	cli := writeExecutable(t, dir, keldCLIName())

	got, err := agentExecutable(func() string { return installed }, func() (string, error) { return cli, nil })
	if err != nil {
		t.Fatalf("agentExecutable: %v", err)
	}
	if got != installed {
		t.Fatalf("resolved %q, want the already-installed %q — a restart repointed the service", got, installed)
	}
}

// NEGATIVE, and the one that must never regress: whatever the inputs, the
// resolved program is the daemon. If this ever returns the CLI again, the
// service definition names something that cannot run and the machine collects
// nothing.
func TestTheResolvedProgramIsNeverTheCLI(t *testing.T) {
	dir := t.TempDir()
	cli := writeExecutable(t, dir, keldCLIName())
	agent := writeExecutable(t, dir, agentFileName())

	cases := []struct {
		name      string
		installed func() string
		exe       string
	}{
		{"no definition installed yet", func() string { return "" }, cli},
		{"a definition that names the CLI (the broken state itself)", func() string { return cli }, cli},
		{"no reader at all", nil, cli},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := agentExecutable(c.installed, func() (string, error) { return c.exe, nil })
			if err != nil {
				t.Fatalf("agentExecutable: %v", err)
			}
			if !isAgentBinary(got) {
				t.Fatalf("resolved %q, which is not the daemon — this is the defect", got)
			}
			if got != agent {
				t.Fatalf("resolved %q, want the sibling daemon %q", got, agent)
			}
		})
	}
}

// ⚠️ A DEFINITION ALREADY NAMING THE CLI MUST NOT BE ADOPTED. Source (1) keeps
// what is installed — but a machine already broken by the old behaviour has a
// plist naming `keld`, and adopting it would make the damage permanent and
// unrepairable by the very command meant to fix it. Covered above as a case,
// and stated here because it is the non-obvious half of "keep what is
// installed".
func TestAnAlreadyBrokenDefinitionIsNotAdopted(t *testing.T) {
	dir := t.TempDir()
	cli := writeExecutable(t, dir, keldCLIName())
	agent := writeExecutable(t, dir, agentFileName())

	got, err := agentExecutable(func() string { return cli }, func() (string, error) { return cli, nil })
	if err != nil {
		t.Fatalf("agentExecutable: %v", err)
	}
	if got != agent {
		t.Fatalf("resolved %q; a plist already naming the CLI was adopted rather than repaired", got)
	}
}

// The ordinary path: the daemon installing itself.
func TestTheDaemonInstallingItselfResolvesToItself(t *testing.T) {
	dir := t.TempDir()
	agent := writeExecutable(t, dir, agentFileName())

	got, err := agentExecutable(func() string { return "" }, func() (string, error) { return agent, nil })
	if err != nil {
		t.Fatalf("agentExecutable: %v", err)
	}
	if got != agent {
		t.Fatalf("resolved %q, want %q", got, agent)
	}
}

// NEGATIVE: with no daemon anywhere, it REFUSES and the caller writes nothing.
//
// ⚠️ Refusing is the safe direction and is the whole design. The alternative —
// writing a definition naming a best guess — is exactly what produced the
// outage: a service that starts, fails, and is restarted forever is strictly
// worse than a restart that says it could not proceed.
func TestWithNoDaemonAnywhereItRefusesRatherThanGuessing(t *testing.T) {
	dir := t.TempDir()
	cli := writeExecutable(t, dir, keldCLIName()) // deliberately no keld-agent beside it

	got, err := agentExecutable(func() string { return "" }, func() (string, error) { return cli, nil })
	if !errors.Is(err, ErrNoAgentBinary) {
		t.Fatalf("err = %v, want ErrNoAgentBinary", err)
	}
	if got != "" {
		t.Fatalf("returned a path (%q) alongside an error; the caller may write it", got)
	}
	if !strings.Contains(err.Error(), agentFileName()) {
		t.Fatalf("error does not name what it looked for: %v", err)
	}
}

// NEGATIVE: a directory or a non-executable file named keld-agent is not a
// daemon. Without the mode check, an empty placeholder or a stray directory
// would be adopted and the service would name something unrunnable — the same
// class of failure by a different route.
func TestANonExecutableSiblingIsNotAccepted(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no execute bit on Windows; isRegularExecutable accepts any regular file there")
	}
	dir := t.TempDir()
	cli := writeExecutable(t, dir, keldCLIName())
	if err := os.WriteFile(filepath.Join(dir, agentFileName()), []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := agentExecutable(func() string { return "" }, func() (string, error) { return cli, nil }); !errors.Is(err, ErrNoAgentBinary) {
		t.Fatalf("err = %v, want ErrNoAgentBinary for a non-executable sibling", err)
	}

	sub := filepath.Join(dir, "d")
	if err := os.MkdirAll(filepath.Join(sub, agentFileName()), 0o755); err != nil {
		t.Fatal(err)
	}
	cli2 := writeExecutable(t, sub, keldCLIName())
	if _, err := agentExecutable(func() string { return "" }, func() (string, error) { return cli2, nil }); !errors.Is(err, ErrNoAgentBinary) {
		t.Fatalf("err = %v, want ErrNoAgentBinary for a DIRECTORY named %s", err, agentFileName())
	}
}

// isAgentBinary matches on the file NAME, in any directory, because the daemon
// lives somewhere different in every layout this project ships. A suffix test
// would wrongly accept `not-keld-agent`; this pins that it does not.
func TestIsAgentBinaryMatchesTheNameAndNotASuffix(t *testing.T) {
	yes := []string{
		"/usr/local/keld/" + agentFileName(),
		"/Users/x/.local/bin/" + agentFileName(),
		"./" + agentFileName(),
	}
	for _, p := range yes {
		if !isAgentBinary(p) {
			t.Fatalf("isAgentBinary(%q) = false, want true", p)
		}
	}
	no := []string{
		"/usr/local/bin/keld",
		"/usr/local/bin/not-" + agentFileName(),
		"/usr/local/bin/" + agentFileName() + "-sidecar",
		"/usr/local/bin/keld-agentx",
		"",
	}
	for _, p := range no {
		if isAgentBinary(p) {
			t.Fatalf("isAgentBinary(%q) = true, want false", p)
		}
	}
}

// keldCLIName is the user-facing CLI's on-disk name — the binary that must
// never end up in a service definition.
func keldCLIName() string {
	if runtime.GOOS == "windows" {
		return "keld.exe"
	}
	return "keld"
}
