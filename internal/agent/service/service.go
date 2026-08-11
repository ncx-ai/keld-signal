// Package service installs keld-agent as a per-user autostart service.
package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// Label is the reverse-DNS service identifier used across platforms.
const Label = "co.keld.agent"

// agentBinaryName is the daemon executable's base name for the current OS.
func agentBinaryName() string {
	if runtime.GOOS == "windows" {
		return "keld-agent.exe"
	}
	return "keld-agent"
}

// resolveAgentBinary picks the keld-agent daemon binary to register as the
// autostart program, given the currently-running executable `exe`. The service
// can be (re)installed from either the keld CLI or keld-agent itself, but the
// program launchd/systemd/schtasks runs must always be keld-agent: the keld CLI
// has no `run` subcommand, so a plist of `keld run` crash-loops the daemon.
//
// Resolution order: (1) if we already are keld-agent, use our own path;
// (2) keld-agent sitting beside us (how the installers lay the payload out);
// (3) keld-agent on PATH. Returns an error if none can be located rather than
// falling back to `exe`, which is what produced the broken `keld run` plist.
func resolveAgentBinary(exe string, exists func(string) bool, lookPath func(string) (string, bool)) (string, error) {
	name := agentBinaryName()
	if filepath.Base(exe) == name {
		return exe, nil
	}
	if cand := filepath.Join(filepath.Dir(exe), name); exists(cand) {
		return cand, nil
	}
	if p, ok := lookPath(name); ok {
		return p, nil
	}
	return "", fmt.Errorf("keld-agent binary not found beside %s or on PATH; reinstall keld", exe)
}

// agentExecPath resolves the keld-agent daemon binary using real filesystem and
// PATH probes. Used to build the autostart program path on every platform.
func agentExecPath() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	return resolveAgentBinary(exe, isRegularFile, func(n string) (string, bool) {
		p, lerr := exec.LookPath(n)
		return p, lerr == nil
	})
}

// isRegularFile reports whether p exists and is a regular file.
func isRegularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// LaunchAgentPlist returns the macOS LaunchAgent plist for the given exec path,
// with launchd stdout/stderr redirected to stdoutPath/stderrPath (absolute
// paths — launchd does not expand "~"). Without these, a crash-looping daemon
// logs nowhere.
func LaunchAgentPlist(execPath, stdoutPath, stderrPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, Label, execPath, stdoutPath, stderrPath)
}

// SystemdUnit returns the systemd --user unit for the given exec path.
func SystemdUnit(execPath string) string {
	return fmt.Sprintf(`[Unit]
Description=Keld enrichment daemon

[Service]
ExecStart=%s run
Restart=on-failure
Nice=10

[Install]
WantedBy=default.target
`, execPath)
}
