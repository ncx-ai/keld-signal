//go:build windows

// Package winproc keeps spawned console programs from putting a window on the
// screen.
//
// ⚠️ **THIS EXISTS BECAUSE THE SAME DEFECT SHIPPED FIVE TIMES IN DIFFERENT
// PLACES.** Every binary here is a CONSOLE program, and when one is started by a
// parent that has no console of its own — a GUI installer, a windowsgui helper,
// a service — Windows ALLOCATES A NEW CONSOLE AND SHOWS IT. The person sees a
// black window appear and vanish, on a product whose entire Windows story is
// that no terminal ever appears.
//
// It has been found and fixed separately in the wizard host, the service
// package's schtasks calls, the daemon's sidecar spawn, the installer's
// postinstall action, and the installer's post-install steps. Each fix was
// correct and none of them generalised, because there was nowhere to put the
// knowledge. This is that place.
//
// ⚠️ HideWindow ALONE IS NOT ENOUGH. It sets STARTF_USESHOWWINDOW/SW_HIDE, which
// asks a window not to be SHOWN once it exists — the console is still allocated,
// and on a real install it appeared anyway. CREATE_NO_WINDOW is what stops the
// console being created at all. Both are set: the flag does the work, and
// HideWindow costs nothing and covers a child that allocates its own console
// later.
package winproc

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW.
const createNoWindow = 0x08000000

// Hide makes cmd run without a console window. Call it before Start/Run/Output.
// Any SysProcAttr the caller already set is preserved; only the window fields
// are added, so it composes with a caller that needs its own flags.
func Hide(cmd *exec.Cmd) {
	if cmd == nil {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
