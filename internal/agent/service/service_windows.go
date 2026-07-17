//go:build windows

package service

import (
	"os/exec"
)

// taskName is the Windows Scheduled Task name.
const taskName = "KeldAgent"

func Install() error {
	exe, err := agentExecPath()
	if err != nil {
		return err
	}
	// Per-user logon task running `keld-agent run`.
	if err := exec.Command("schtasks", "/Create", "/F",
		"/SC", "ONLOGON",
		"/TN", taskName,
		"/TR", `"`+exe+`" run`,
	).Run(); err != nil {
		return err
	}
	// Run it now (don't wait for next logon), restarting any running instance so a
	// REINSTALL picks up the newly-installed binary.
	_ = exec.Command("schtasks", "/End", "/TN", taskName).Run() // no-op if not running
	return exec.Command("schtasks", "/Run", "/TN", taskName).Run()
}

func Uninstall() error {
	return exec.Command("schtasks", "/Delete", "/F", "/TN", taskName).Run()
}

// Start runs the scheduled task now.
func Start() error { return exec.Command("schtasks", "/Run", "/TN", taskName).Run() }

// Stop ends the running task instance.
func Stop() error { return exec.Command("schtasks", "/End", "/TN", taskName).Run() }

// Restart ends then re-runs the task (picks up a newly-installed binary).
func Restart() error {
	_ = exec.Command("schtasks", "/End", "/TN", taskName).Run()
	return exec.Command("schtasks", "/Run", "/TN", taskName).Run()
}

func Status() (string, error) {
	out, err := exec.Command("schtasks", "/Query", "/TN", taskName).CombinedOutput()
	if err != nil {
		return "not installed", nil
	}
	return string(out), nil
}
