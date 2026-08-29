//go:build windows

package service

import (
	"os"
	"os/exec"
)

// taskName is the Windows Scheduled Task name.
const taskName = "KeldAgent"

func Install() error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	return InstallAt(exe)
}

// InstallAt registers the task to run execPath, rather than whatever binary
// happens to be running now. See the darwin implementation's comment: the two
// differ only for an auto-update that had to migrate to a writable directory.
func InstallAt(exe string) error {
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

// Uninstall ENDS the task before deleting it. `/Delete /F` removes the
// registration but does not terminate an instance that is already running, and
// the daemon is running by definition on any machine that installed it — so a
// delete-only uninstall left keld-agent.exe alive holding its own binary open.
// The installer's uninstaller then could not delete the files it was uninstalling.
// `Restart` above has always done End-then-Run for the same reason; this is that
// pairing applied to the one path that was missing it.
//
// The `/End` is best-effort: a task that is registered but not running makes it a
// no-op, and its failure must not stop the delete — the delete is what uninstall
// actually promises.
func Uninstall() error {
	_ = exec.Command("schtasks", "/End", "/TN", taskName).Run() // no-op if not running
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
