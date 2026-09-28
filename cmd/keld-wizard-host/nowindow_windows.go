//go:build windows

package main

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW: run a console application with no console
// window of its own.
const createNoWindow = 0x08000000

// hideChildWindow is what keeps the wizard looking like a wizard.
//
// ⚠️ **WITHOUT IT, EVERY STEP FLASHES A CONSOLE WINDOW ON SCREEN.** This helper
// is built -H windowsgui so it has no console of its own — and that is exactly
// what causes the problem rather than avoiding it: `keld.exe` is a CONSOLE
// subsystem binary, and when a console child is started by a parent that has no
// console, Windows allocates a NEW one and shows it. The wizard page runs three
// or four such children in a row (identity, clipboard, tools, login), so the
// person sees a series of black windows appear and vanish.
//
// Measured on a real install 2026-09-15, reported as "it launched and rapidly
// closed a series of windows" — on the one screen whose entire purpose is that
// no console ever appears. Inno's own `Exec(…, SW_HIDE, …)` does not help: that
// hides the HELPER's window, and the helper is not what was showing one.
//
// HideWindow alone is not enough either — it sets STARTF_USESHOWWINDOW/SW_HIDE,
// which asks a window not to be shown once it exists. CREATE_NO_WINDOW is what
// stops the console being created at all.
func hideChildWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
}
