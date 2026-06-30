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
	exe, err := os.Executable()
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
	return exec.Command("systemctl", "--user", "enable", "--now", "keld-agent.service").Run()
}

func Uninstall() error {
	_ = exec.Command("systemctl", "--user", "disable", "--now", "keld-agent.service").Run()
	return os.Remove(unitPath())
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
