//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	p := plistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(p, []byte(LaunchAgentPlist(exe)), 0o644); err != nil {
		return err
	}
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	// bootout then bootstrap = a restart, so a REINSTALL over a running agent picks up
	// the newly-installed binary (launchd starts whatever is at the plist's program path).
	_ = exec.Command("launchctl", "bootout", uid, p).Run() // ignore if not loaded
	return exec.Command("launchctl", "bootstrap", uid, p).Run()
}

func Uninstall() error {
	p := plistPath()
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootout", uid, p).Run()
	return os.Remove(p)
}

// Start loads the agent if needed, then (re)starts the job.
func Start() error {
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	_ = exec.Command("launchctl", "bootstrap", uid, plistPath()).Run() // no-op if already loaded
	return exec.Command("launchctl", "kickstart", uid+"/"+Label).Run()
}

// Stop unloads the agent (KeepAlive would otherwise respawn it); Install/Start reload it.
func Stop() error {
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	return exec.Command("launchctl", "bootout", uid, plistPath()).Run()
}

// Restart kills and restarts the running job (picks up a newly-installed binary).
func Restart() error {
	return exec.Command("launchctl", "kickstart", "-k", fmt.Sprintf("gui/%d/%s", os.Getuid(), Label)).Run()
}

func Status() (string, error) {
	out, err := exec.Command("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), Label)).CombinedOutput()
	if err != nil {
		return "not running", nil
	}
	return string(out), nil
}
