package service

import (
	"strings"
	"testing"
)

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
