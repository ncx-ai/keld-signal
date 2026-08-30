//go:build !windows

package agentcli

// hideOwnConsole is a Windows-only concern: launchd and systemd start the daemon
// with no controlling terminal, so there has never been a window to detach from.
func hideOwnConsole() {}
