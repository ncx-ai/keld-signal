//go:build darwin || linux

package daemon

import "os/exec"

// reapStaleSidecars terminates any running sidecar process whose full command
// line matches binPath — an orphan left by a prior daemon that died without
// cleaning up its child (e.g. launchd `kickstart -k` SIGKILL). Under
// single-instance service management any such process is stale, so reaping
// before spawning guarantees exactly one sidecar per daemon. Best-effort: a
// no-match exit from pkill is ignored.
func reapStaleSidecars(binPath string) {
	reapStaleSidecarsWith(binPath, func(name string, args ...string) error {
		return exec.Command(name, args...).Run()
	})
}

func reapStaleSidecarsWith(binPath string, run func(name string, args ...string) error) {
	_ = run("pkill", "-f", binPath)
}
