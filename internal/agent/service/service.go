// Package service installs keld-agent as a per-user autostart service.
package service

import "fmt"

// Label is the reverse-DNS service identifier used across platforms.
const Label = "co.keld.agent"

// LaunchAgentPlist returns the macOS LaunchAgent plist for the given exec path,
// with launchd stdout/stderr redirected to stdoutPath/stderrPath (absolute
// paths — launchd does not expand "~"). Without these, a crash-looping daemon
// logs nowhere.
//
// KeepAlive is the SuccessfulExit=false DICTIONARY, not an unconditional
// <true/>. Unconditional KeepAlive respawns the job even after a clean exit,
// which turned "the service was registered before onboarding ran" (the
// documented macOS pkg order) into 69 spawns in 12 minutes on a tester's
// machine. Keyed on SuccessfulExit, a clean `exit 0` ends the job while a
// non-zero exit — the daemon dying for a real reason — is still restarted.
// The daemon no longer exits at all when unconfigured (it idles and re-checks,
// see daemon.awaitConfig), so this is the second line of defence: any *other*
// permanently-fatal startup error now costs one spawn, not an endless series.
func LaunchAgentPlist(execPath, stdoutPath, stderrPath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>%s</string>
  <key>ProgramArguments</key>
  <array><string>%s</string><string>run</string></array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key>
  <dict>
    <key>SuccessfulExit</key><false/>
  </dict>
  <key>ProcessType</key><string>Background</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, Label, execPath, stdoutPath, stderrPath)
}

// SystemdUnit returns the systemd --user unit for the given exec path.
//
// Restart=on-failure (never `always`) is the systemd equivalent of the plist's
// KeepAlive/SuccessfulExit dictionary: a clean exit is final, a crash restarts.
// Do NOT add RestartSec here — systemd's default start-limit (5 starts / 10s)
// is what bounds a genuine crash loop, and spacing restarts out past that
// window would convert a bounded burst into an unbounded loop.
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
