//go:build darwin || linux

package daemon

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts the spawned sidecar in a process group of its OWN, led
// by itself (Setpgid with a zero Pgid ⇒ pgid == pid).
//
// ⚠️ The group is the ONLY handle the kill path has on the sidecar's children,
// and they are where this project's memory lives: the GLiNER2 inference worker
// (~2.9 GB measured live) and the text encoder (~1.7 GB resident, ~2.3 GB
// peak). Go's os/exec signals a PID, never a tree, so a SIGKILL to the sidecar
// alone leaves both reparented to init/systemd holding those gigabytes
// indefinitely — observed on this machine, and independently in the load-test
// harness (five orphans, 1.0-2.9 GB each; see loadtest/harness.py's stop()).
// Signalling the negated pgid reaches grandchildren the daemon never knew
// existed. Pdeathsig would cover Linux only, which is why it is not used here.
//
// It does NOT break the frozen PyInstaller binary's multiprocessing spawn. A
// spawned child inherits its parent's process group, so the re-exec'd
// bootstrap (AGENTS.md → freeze_support) lands in the same group — which is
// precisely why the group reaches it.
//
// ⚠️ It DOES change signal delivery: the sidecar is no longer in the daemon's
// (or a dev terminal's) foreground group, so a Ctrl-C at a `keld-agent run`
// prompt no longer reaches it directly. That is not a regression — the SIGINT
// still cancels the daemon's context, which now runs stopChild's graceful
// SIGTERM, and the sidecar's lifespan teardown finally executes instead of
// being pre-empted. A `kill -9` of the daemon still orphans the sidecar, as it
// always did; reapStaleSidecars is the recovery for that on the next start.
//
// Any SysProcAttr a caller already set is preserved; only Setpgid is added.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// childGroup returns the process group id to sweep for pid, and whether that
// group is safe to signal.
//
// ⚠️ The pgid == pid test is a SAFETY GUARD, not bookkeeping. kill(-pgid) with
// a pgid we do not own signals the DAEMON'S OWN process group — suicide, and
// in a developer's terminal the whole foreground job with it. So a child that
// did not end up leading its own group (a spawn func that replaced
// SysProcAttr, or a Setpgid the kernel refused) reports false, and the caller
// falls back to signalling the single PID: exactly the pre-fix behaviour, and
// never worse than it.
func childGroup(pid int) (int, bool) {
	pgid, err := syscall.Getpgid(pid)
	if err != nil {
		return 0, false
	}
	return pgid, pgid == pid
}

// terminateChild asks the sidecar PID — and only that PID — to shut down.
//
// ⚠️ Deliberately not the group. The sidecar's lifespan teardown is already
// correct (it calls wm.shutdown() and _TEXT_SOURCE.shutdown()); the bug this
// file fixes is that SIGKILL meant it never ran. SIGTERMing the whole group
// would kill those children out from under the very teardown we are trying to
// let finish — TextSource.shutdown drains an in-flight encode against a child
// that would no longer be there. The group is swept forcefully afterwards, so
// anything the parent misses is still reaped.
func terminateChild(pid int) error {
	return syscall.Kill(pid, syscall.SIGTERM)
}

// killProcessTree SIGKILLs every survivor in the child's process group. Called
// unconditionally after the graceful attempt, so a parent that exited cleanly
// but left a straggler behind is still swept; against an empty group this is a
// no-op ESRCH.
//
// pgid is captured by the caller while the child was still alive, because
// cmd.Wait() may have reaped it by now. A reaped pid is eligible for reuse, so
// in principle the sweep could land on an unrelated group — that would need
// the OS to recycle this exact pid AND the new holder to become a group leader
// within the microseconds between exit and sweep. Inherent to any supervisor
// that reaps on a separate goroutine, and accepted here for the same reason.
func killProcessTree(pid, pgid int, group bool) error {
	if group {
		return syscall.Kill(-pgid, syscall.SIGKILL)
	}
	return syscall.Kill(pid, syscall.SIGKILL)
}

// gracefulStopSupported reports whether terminateChild can do anything useful
// on this platform. Unix has SIGTERM, so the graceful half of stopChild runs.
func gracefulStopSupported() bool { return true }
