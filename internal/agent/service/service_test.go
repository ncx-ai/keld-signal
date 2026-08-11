package service

import (
	"path/filepath"
	"strings"
	"testing"
)

// The service can be (re)installed from either the keld CLI or keld-agent, but
// the launchd/systemd/schtasks program must always be keld-agent — the keld CLI
// has no `run` command, so a plist of `keld run` crash-loops the daemon.
func TestResolveAgentBinaryPrefersSiblingWhenInvokedFromKeld(t *testing.T) {
	agent := filepath.Join("/usr/local/bin", agentBinaryName())
	exists := func(p string) bool { return p == agent }
	lookPath := func(string) (string, bool) { return "", false }

	got, err := resolveAgentBinary("/usr/local/bin/keld", exists, lookPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != agent {
		t.Fatalf("resolveAgentBinary from keld CLI = %q, want %q", got, agent)
	}
}

func TestResolveAgentBinaryUsesOwnPathWhenInvokedFromAgent(t *testing.T) {
	agent := filepath.Join("/opt/keld", agentBinaryName())
	exists := func(string) bool { return false }
	lookPath := func(string) (string, bool) { return "", false }

	got, err := resolveAgentBinary(agent, exists, lookPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != agent {
		t.Fatalf("resolveAgentBinary from keld-agent = %q, want %q", got, agent)
	}
}

func TestResolveAgentBinaryFallsBackToPATH(t *testing.T) {
	onPath := filepath.Join("/somewhere/on/path", agentBinaryName())
	exists := func(string) bool { return false }
	lookPath := func(string) (string, bool) { return onPath, true }

	got, err := resolveAgentBinary("/usr/local/bin/keld", exists, lookPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != onPath {
		t.Fatalf("resolveAgentBinary PATH fallback = %q, want %q", got, onPath)
	}
}

func TestResolveAgentBinaryErrorsWhenAgentMissing(t *testing.T) {
	exists := func(string) bool { return false }
	lookPath := func(string) (string, bool) { return "", false }

	if _, err := resolveAgentBinary("/usr/local/bin/keld", exists, lookPath); err == nil {
		t.Fatalf("expected error when keld-agent cannot be located, got nil")
	}
}

func TestLaunchAgentPlistContainsExecAndLabel(t *testing.T) {
	p := LaunchAgentPlist(
		"/usr/local/bin/keld-agent",
		"/home/u/.keld/logs/agent.out.log",
		"/home/u/.keld/logs/agent.err.log",
	)
	if !strings.Contains(p, "<string>/usr/local/bin/keld-agent</string>") {
		t.Fatalf("plist missing exec path:\n%s", p)
	}
	if !strings.Contains(p, "co.keld.agent") {
		t.Fatalf("plist missing label:\n%s", p)
	}
	if !strings.Contains(p, "<key>RunAtLoad</key>") {
		t.Fatalf("plist missing RunAtLoad:\n%s", p)
	}
	if !strings.Contains(p, "<key>StandardOutPath</key><string>/home/u/.keld/logs/agent.out.log</string>") {
		t.Fatalf("plist missing StandardOutPath:\n%s", p)
	}
	if !strings.Contains(p, "<key>StandardErrorPath</key><string>/home/u/.keld/logs/agent.err.log</string>") {
		t.Fatalf("plist missing StandardErrorPath:\n%s", p)
	}
}

func TestSystemdUnitContainsExecAndRestart(t *testing.T) {
	u := SystemdUnit("/home/u/.local/bin/keld-agent")
	if !strings.Contains(u, "ExecStart=/home/u/.local/bin/keld-agent run") {
		t.Fatalf("unit missing ExecStart:\n%s", u)
	}
	if !strings.Contains(u, "Restart=on-failure") {
		t.Fatalf("unit missing Restart:\n%s", u)
	}
}
