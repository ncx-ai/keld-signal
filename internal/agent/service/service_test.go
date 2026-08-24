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
	// Restart=always would respawn a clean exit — the systemd twin of the
	// unconditional-KeepAlive crashloop. And RestartSec must stay unset: pacing
	// restarts past systemd's 10s start-limit window unbounds a crash loop.
	if strings.Contains(u, "Restart=always") {
		t.Fatalf("unit restarts on clean exit:\n%s", u)
	}
	if strings.Contains(u, "RestartSec") {
		t.Fatalf("unit sets RestartSec, defeating the start limit:\n%s", u)
	}
}

// A launchd job whose KeepAlive is an unconditional <true/> is respawned even
// after a clean exit, which is how an unconfigured agent produced 69 spawns in
// 12 minutes on a tester's machine. KeepAlive must be a dictionary keyed on
// SuccessfulExit so a clean exit ends the job while a real crash (non-zero) is
// still restarted.
func TestLaunchAgentPlistDoesNotRespawnAfterCleanExit(t *testing.T) {
	p := LaunchAgentPlist("/usr/local/bin/keld-agent", "/o.log", "/e.log")
	if strings.Contains(p, "<key>KeepAlive</key><true/>") {
		t.Fatalf("KeepAlive is unconditional — a clean exit is respawned forever:\n%s", p)
	}
	if !strings.Contains(p, "<key>KeepAlive</key>") {
		t.Fatalf("plist dropped KeepAlive entirely — a real crash would not restart:\n%s", p)
	}
	// The dictionary form: restart only when the last exit was NOT successful.
	norm := strings.Join(strings.Fields(p), " ")
	if !strings.Contains(norm, "<key>KeepAlive</key> <dict> <key>SuccessfulExit</key><false/> </dict>") {
		t.Fatalf("KeepAlive is not the SuccessfulExit=false dictionary:\n%s", p)
	}
}
