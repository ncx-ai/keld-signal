//go:build darwin || linux

package daemon

import (
	"os/exec"
	"path/filepath"
)

// reapStaleSidecars terminates any running sidecar process — an orphan left by
// a prior daemon that died without cleaning up its child (e.g. launchd
// `kickstart -k` SIGKILL, or a reinstall). Under single-instance service
// management any such process is stale, so reaping before spawning guarantees
// exactly one sidecar per daemon. Best-effort: a no-match exit is ignored.
//
// Matched on the BASENAME of binPath, not binPath itself. The installers place
// the sidecar in different directories — the macOS pkg under /usr/local/keld,
// `curl | sh` under ~/.local/bin — and sidecarBinPath() resolves whichever this
// daemon can see. Matching the full path therefore reaped only sidecars from
// that one directory and left the OTHER install's child running indefinitely:
// a machine installed both ways carried a 13-day-old orphan across several
// reinstalls, holding a port and its RSS the whole time, which is precisely the
// "exactly one sidecar per daemon" this promised. Mirrors reap_windows.go,
// which has always matched by image name.
func reapStaleSidecars(binPath string) {
	reapStaleSidecarsWith(binPath, func(name string, args ...string) error {
		return exec.Command(name, args...).Run()
	})
}

func reapStaleSidecarsWith(binPath string, run func(name string, args ...string) error) {
	// filepath.Base("") is ".", and `pkill -f .` matches EVERY process on the
	// machine. The one call site only reaps after sidecarBinPath() reported a
	// hit, so this cannot fire today — but the blast radius of being wrong is
	// the user's whole login session, so it is checked rather than assumed.
	if binPath == "" {
		return
	}
	// Safe against self-match: the daemon's own command line is `keld-agent run`,
	// which does not contain the sidecar basename `keld-agent-sidecar`.
	_ = run("pkill", "-f", filepath.Base(binPath))
}
