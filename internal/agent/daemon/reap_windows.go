//go:build windows

package daemon

import "os/exec"

// reapStaleSidecars terminates any orphaned sidecar process by image name.
// Best-effort; a no-match exit is ignored. (binPath is unused on Windows —
// taskkill matches by image name.)
func reapStaleSidecars(binPath string) {
	_ = exec.Command("taskkill", "/F", "/IM", "keld-agent-sidecar.exe").Run()
}
