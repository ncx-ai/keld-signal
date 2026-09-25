//go:build windows

package service

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW: run a console application with no console
// window of its own.
const createNoWindow = 0x08000000

// command is exec.Command with the console window suppressed, and every process
// this package starts MUST go through it.
//
// ⚠️ **WITHOUT IT, INSTALLING FLASHES CONSOLE WINDOWS AT THE PERSON.** Every
// helper here is a console program — schtasks, taskkill — and `keld-agent
// install` runs several in a row (/Create, /End, /Run). Windows gives each one a
// console window, so the install pops a black window per call. Reported on a
// real install as "a terminal window opened and closed twice", on a flow whose
// entire premise is that no terminal ever appears.
//
// Inno's `Flags: runhidden` does NOT help: it hides keld-agent's own window, and
// keld-agent is not what was showing one — its CHILDREN are. This is the same
// defect, and the same fix, as cmd/keld-wizard-host/nowindow_windows.go; that
// file has the longer explanation of why HideWindow alone is insufficient
// (it asks a window not to be shown once created; CREATE_NO_WINDOW stops it
// being created at all). Both are set here for the same belt-and-braces reason.
func command(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow,
	}
	return cmd
}
