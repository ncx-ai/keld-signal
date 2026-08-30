//go:build windows

package service

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
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
	// Per-user logon task running `keld-agent run`, registered from a task
	// document rather than `/SC ONLOGON` — see taskxml.go for why that flag
	// cannot work in an unelevated installer.
	xmlPath, cleanup, err := writeTaskXML(exe)
	if err != nil {
		return err
	}
	defer cleanup()

	// ⚠️ CAPTURE schtasks' OWN MESSAGE. This used to be a bare .Run(), which
	// discards stderr, so the registration failure above surfaced as the single
	// word "exit status 1" — and the installer runs this hidden, so that word
	// went nowhere either. The only evidence anywhere was `schtasks /Query`
	// reporting no such task. schtasks explains itself perfectly well
	// ("ERROR: Access is denied."); the defect was never asking it.
	if out, err := exec.Command("schtasks", "/Create", "/F",
		"/TN", taskName, "/XML", xmlPath,
	).CombinedOutput(); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("schtasks /Create %s: %w: %s", taskName, err, msg)
		}
		return fmt.Errorf("schtasks /Create %s: %w", taskName, err)
	}
	// Run it now (don't wait for next logon), restarting any running instance so a
	// REINSTALL picks up the newly-installed binary.
	_ = exec.Command("schtasks", "/End", "/TN", taskName).Run() // no-op if not running
	return exec.Command("schtasks", "/Run", "/TN", taskName).Run()
}

// writeTaskXML writes the registration document to a temp file and returns its
// path plus a cleanup func. UTF-16 because the document says it is; see
// utf16LEWithBOM.
func writeTaskXML(exe string) (string, func(), error) {
	f, err := os.CreateTemp("", "keld-task-*.xml")
	if err != nil {
		return "", func() {}, err
	}
	name := f.Name()
	cleanup := func() { _ = os.Remove(name) }
	if _, err := f.Write(utf16LEWithBOM(taskXMLFor(taskUser(), exe))); err != nil {
		f.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := f.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return name, cleanup, nil
}

// taskUser names the account the trigger is scoped to. USERNAME first because
// that is the exact form verified against schtasks on the affected machine;
// user.Current() is the fallback and returns the qualified DOMAIN\user form,
// which Task Scheduler also accepts.
func taskUser() string {
	if u := os.Getenv("USERNAME"); u != "" {
		return u
	}
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return ""
}

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
