// Package service installs keld-agent as a per-user autostart service.
package service

import "fmt"

// Label is the reverse-DNS service identifier used across platforms.
const Label = "co.keld.agent"

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
