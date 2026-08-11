//go:build linux

package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func unitPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user", "keld-agent.service")
}

func Install() error {
	exe, err := agentExecPath()
	if err != nil {
		return err
	}
	p := unitPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(SystemdUnit(exe)), 0o644); err != nil {
		return err
	}
	_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	if err := exec.Command("systemctl", "--user", "enable", "keld-agent.service").Run(); err != nil {
		return err
	}
	// restart (not `enable --now`) so a REINSTALL over a running daemon picks up the
	// newly-installed binary; on a fresh install `restart` just starts the stopped unit.
	return exec.Command("systemctl", "--user", "restart", "keld-agent.service").Run()
}

func Uninstall() error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", "keld-agent.service").Run()
	return os.Remove(unitPath())
}

// Start starts the (installed) service; no-op-ish if already running.
func Start() error { return exec.Command("systemctl", "--user", "start", "keld-agent.service").Run() }

// Stop stops the running service (leaves it installed/enabled).
func Stop() error { return exec.Command("systemctl", "--user", "stop", "keld-agent.service").Run() }

// Restart restarts the service, picking up a newly-installed binary.
func Restart() error {
	return exec.Command("systemctl", "--user", "restart", "keld-agent.service").Run()
}

func Status() (string, error) {
	out, err := exec.Command("systemctl", "--user", "is-active", "keld-agent.service").CombinedOutput()
	s := strings.TrimSpace(string(out))
	if s == "" {
		if err != nil {
			return "not running", nil
		}
		return "unknown", nil
	}
	return s, nil
}
