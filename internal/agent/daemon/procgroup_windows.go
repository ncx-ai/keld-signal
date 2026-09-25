//go:build windows

package daemon

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"strconv"
)

// ⚠️ WINDOWS IS THE PARTIAL FIX, AND THIS COMMENT IS THE DISCLOSURE.
//
// The Unix sibling of this file gets the full treatment: a process group, a
// graceful SIGTERM that lets the sidecar's lifespan teardown run, and a
// forceful group sweep. Windows gets ONE of those three — the tree reap — and
// the reasons for stopping there are:
//
//   - There is no SIGTERM. A console process can be asked to stop with
//     GenerateConsoleCtrlEvent, but only within a shared console group, and the
//     daemon runs as a service with no console at all. So the sidecar's
//     lifespan teardown does NOT run on Windows: wm.shutdown() and
//     _TEXT_SOURCE.shutdown() are still unreachable here.
//   - The correct answer is a JOB OBJECT (CreateJobObject +
//     JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE), which reaps the tree even for a
//     grandchild that has already been reparented, and would also survive a
//     hard kill of the daemon. That is a genuine piece of work — golang.org/x/sys
//     plumbing, handle ownership across the spawn, and no way to exercise it
//     from a Linux dev machine — and it is NOT done.
//
// What IS done: taskkill /T walks the OS's parent-child tree from the sidecar
// down, which covers the multiprocessing children that hold the memory
// (~2.9 GB GLiNER2 worker, ~1.7-2.3 GB encoder) because they are direct
// descendants of a sidecar that is still alive when we signal. It follows the
// precedent already in reap_windows.go rather than inventing a mechanism.
// Residual gap: a descendant whose intermediate parent has already exited is
// not reachable by the tree walk, and a job object would have caught it.

// createNoWindow is CREATE_NO_WINDOW: run a console application with no console
// window of its own.
const createNoWindow = 0x08000000

// setProcessGroup does no grouping on Windows — there are no Unix process
// groups, and the job object that would be the equivalent is deliberately not
// implemented (see the file comment).
//
// ⚠️ **IT DOES SUPPRESS THE CHILD'S CONSOLE WINDOW, AND THAT IS NOT COSMETIC.**
// The sidecar is a console binary. The daemon runs as a scheduled task with no
// console of its own, and when a console child is started by a parent that has
// none, Windows ALLOCATES A NEW ONE AND SHOWS IT. So starting the sidecar put a
// blank black window on the person's desktop and left it there for as long as
// the daemon ran — reported on a real install as "another terminal opened,
// blank, and just sat there".
//
// This is the same defect and the same fix as
// cmd/keld-wizard-host/nowindow_windows.go and internal/agent/service; that
// first file carries the full explanation of why HideWindow alone is not
// enough. It lives in this function because this is the one place the
// supervisor calls before every spawn, so no future spawn site can forget it.
func setProcessGroup(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}

// childGroup has no meaning on Windows; the caller only forwards these values
// to killProcessTree, which ignores them.
func childGroup(pid int) (int, bool) { return 0, false }

// gracefulStopSupported is false: with no SIGTERM equivalent reachable from a
// console-less service, stopChild skips the graceful half entirely rather than
// burning its grace period waiting for a signal it never sent. Windows
// therefore keeps exactly the old timing and goes straight to the forced kill.
func gracefulStopSupported() bool { return false }

// terminateChild is unreachable on Windows (gracefulStopSupported is false).
// It exists so stopChild compiles from one source file on every platform.
func terminateChild(pid int) error {
	return errors.New("graceful stop unsupported on windows")
}

// killProcessTree force-kills the sidecar and every descendant the OS can walk
// to from it. pgid/group are ignored (see childGroup).
func killProcessTree(pid, pgid int, group bool) error {
	// /T is the whole point — it is what makes this a tree kill rather than
	// the pid-only Process.Kill() that leaked gigabytes.
	kill := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := kill.Run(); err != nil {
		// taskkill missing or refused: fall back to the pre-fix behaviour so a
		// stop is never worse than it used to be.
		p, ferr := os.FindProcess(pid)
		if ferr != nil {
			return err
		}
		return p.Kill()
	}
	return nil
}
