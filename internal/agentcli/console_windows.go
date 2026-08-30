//go:build windows

package agentcli

import (
	"syscall"
	"unsafe"
)

// Hiding the daemon's console window.
//
// `keld-agent run` is a CONSOLE binary — it has to be, because the same
// executable is the CLI (`install`, `status`, `uninstall`) whose output the
// installer's onboarding reads. So when Task Scheduler launches it at logon,
// Windows gives it a console and the user gets a black window full of daemon
// logs, at every single logon. That is what a background agent must not do.
//
// ⚠️ IT MUST NOT HIDE A CONSOLE IT DOES NOT OWN. A developer running
// `keld-agent run` in their own terminal is attached to THAT terminal, and
// hiding it would make the user's own window vanish mid-session — a far worse
// bug than the one being fixed. GetConsoleProcessList is the discriminator:
// a console created FOR this process has exactly one process attached (us),
// while a shared one also has cmd.exe or the shell. Anything ambiguous (an
// error, no console at all) declines to hide, so the failure direction is a
// visible window rather than a vanished one.
//
// The window is HIDDEN, not freed. FreeConsole would detach the process from it
// and every subsequent log write would fail; hiding leaves the console object
// intact, so logging keeps working exactly as before and only the window is
// gone. That matters because there is no file sink: the way to watch this
// daemon's logs on Windows is to run `keld-agent run` in a terminal yourself,
// which the ownership check above deliberately leaves visible.
const swHide = 0

func hideOwnConsole() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	user32 := syscall.NewLazyDLL("user32.dll")

	getConsoleWindow := kernel32.NewProc("GetConsoleWindow")
	getConsoleProcessList := kernel32.NewProc("GetConsoleProcessList")
	showWindow := user32.NewProc("ShowWindow")

	hwnd, _, _ := getConsoleWindow.Call()
	if hwnd == 0 {
		return // no console attached: nothing to hide
	}

	// Room for more than one pid: the count is what matters, and asking for a
	// single slot on a shared console returns the REQUIRED size rather than 1,
	// which reads identically to "we are alone" if the buffer is too small to
	// tell them apart.
	var pids [8]uint32
	n, _, _ := getConsoleProcessList.Call(uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)))
	if n != 1 {
		// 0 == the call failed; >1 == somebody else's console. Both decline.
		return
	}
	showWindow.Call(hwnd, swHide)
}
