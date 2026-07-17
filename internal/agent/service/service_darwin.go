//go:build darwin

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ncx-ai/keld-signal/internal/paths"
)

func plistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

func Install() error {
	exe, err := agentExecPath()
	if err != nil {
		return err
	}
	p := plistPath()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(paths.AgentLogDir(), 0o755); err != nil {
		return err
	}
	plist := LaunchAgentPlist(exe, paths.AgentStdoutLog(), paths.AgentStderrLog())
	if err := os.WriteFile(p, []byte(plist), 0o644); err != nil {
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

// syncPlist ensures the plist at path equals want. If it already matches,
// nothing happens beyond ensuring logDir exists (returns false). Otherwise —
// differing, missing, or unreadable — it also creates the plist directory,
// writes want, and reloads the job via reload (returns true). logDir is
// ensured unconditionally, even on the already-current no-op path: launchd
// does not create the parent directory of StandardErrorPath/StandardOutPath,
// so if it were missing the job would fail to spawn silently. write/reload
// are seams; production wires writeFile (an os.WriteFile adapter) and
// reloadJob (a launchctl bootout+bootstrap).
func syncPlist(path, logDir, want string, write func(string, []byte) error, reload func() error) (bool, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return false, err
	}
	if cur, err := os.ReadFile(path); err == nil && string(cur) == want {
		return false, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	if err := write(path, []byte(want)); err != nil {
		return false, err
	}
	return true, reload()
}

// writeFile adapts os.WriteFile to the write seam's (path, data) signature,
// fixing the file mode used for the plist.
func writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

// currentPlist is the plist this binary should be installed with. The program
// is always keld-agent (resolved via agentExecPath), never the invoking binary,
// so `keld signal start` and `keld-agent restart` converge on the same plist
// instead of fighting over `keld run` vs `keld-agent run`.
func currentPlist() (string, error) {
	exe, err := agentExecPath()
	if err != nil {
		return "", err
	}
	return LaunchAgentPlist(exe, paths.AgentStdoutLog(), paths.AgentStderrLog()), nil
}

// reloadJob adopts an on-disk plist change: bootout then bootstrap.
func reloadJob() error {
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	p := plistPath()
	_ = exec.Command("launchctl", "bootout", uid, p).Run() // ignore if not loaded
	return exec.Command("launchctl", "bootstrap", uid, p).Run()
}

// runCmd is the seam for launchctl invocations so startJob/restartJob are
// unit-testable without shelling out.
type runCmd func(name string, args ...string) error

// launchctlRun runs a launchctl command with no stdio, returning its error.
func launchctlRun(name string, args ...string) error { return exec.Command(name, args...).Run() }

// startJob enables, loads, and kickstarts the launchd job. It runs `launchctl
// enable` first because launchd refuses to bootstrap a job carrying a stale
// `disabled` override (bootstrap fails with error 5), after which `kickstart`
// fails with a cryptic exit 113 — which is all the caller used to see, since
// the bootstrap error was discarded. bootstrap errors remain benign (the job
// may already be loaded) and are ignored; a kickstart failure is the real
// "did not start" signal and is surfaced.
func startJob(run runCmd, uid, plist string) error {
	target := uid + "/" + Label
	_ = run("launchctl", "enable", target)
	_ = run("launchctl", "bootstrap", uid, plist)
	if err := run("launchctl", "kickstart", target); err != nil {
		return fmt.Errorf("launchctl kickstart %s: %w", target, err)
	}
	return nil
}

// restartJob clears any disabled override, ensures the job is loaded, then
// force-kickstarts it (`-k` kills a running daemon so a new binary is picked
// up). Like startJob, it surfaces a kickstart failure rather than swallowing it.
func restartJob(run runCmd, uid, plist string) error {
	target := uid + "/" + Label
	_ = run("launchctl", "enable", target)
	_ = run("launchctl", "bootstrap", uid, plist)
	if err := run("launchctl", "kickstart", "-k", target); err != nil {
		return fmt.Errorf("launchctl kickstart -k %s: %w", target, err)
	}
	return nil
}

// Start loads the agent if needed, then (re)starts the job. It first syncs a
// stale plist so an agent installed before log paths existed adopts them.
func Start() error {
	want, err := currentPlist()
	if err != nil {
		return err
	}
	if _, err := syncPlist(plistPath(), paths.AgentLogDir(), want, writeFile, reloadJob); err != nil {
		return err
	}
	return startJob(launchctlRun, fmt.Sprintf("gui/%d", os.Getuid()), plistPath())
}

// Stop unloads the agent (KeepAlive would otherwise respawn it); Install/Start reload it.
func Stop() error {
	uid := fmt.Sprintf("gui/%d", os.Getuid())
	return exec.Command("launchctl", "bootout", uid, plistPath()).Run()
}

// Restart kills and restarts the running job (picks up a newly-installed
// binary). It first syncs a stale plist so log paths are adopted.
func Restart() error {
	want, err := currentPlist()
	if err != nil {
		return err
	}
	if _, err := syncPlist(plistPath(), paths.AgentLogDir(), want, writeFile, reloadJob); err != nil {
		return err
	}
	return restartJob(launchctlRun, fmt.Sprintf("gui/%d", os.Getuid()), plistPath())
}

func Status() (string, error) {
	out, err := exec.Command("launchctl", "print", fmt.Sprintf("gui/%d/%s", os.Getuid(), Label)).CombinedOutput()
	if err != nil {
		return "not running", nil
	}
	return string(out), nil
}
