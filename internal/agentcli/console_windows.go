//go:build windows

package agentcli

import (
	"syscall"
	"unsafe"
)

// Detaching the daemon from the console Task Scheduler gives it.
//
// `keld-agent run` is a CONSOLE binary — it has to be, because the same
// executable is the CLI (`install`, `status`, `uninstall`) whose output the
// installer's onboarding reads, so -H=windowsgui is not available. When Task
// Scheduler launches it at logon, Windows gives it a console and the user gets a
// black window full of daemon logs at every single logon.
//
// ⚠️ THE FIRST ATTEMPT AT THIS GUESSED, AND GUESSED WRONG ON A REAL MACHINE. It
// called ShowWindow(SW_HIDE) only when GetConsoleProcessList reported exactly one
// attached process — the idea being that a console created FOR this process has
// only this process on it, while a developer's own terminal also has the shell.
// That guard has a SILENT DECLINE: any answer other than 1, including an error,
// skips the hide. Shipped in v2.0.1, the window still appeared, and nothing on
// the machine could say which branch had been taken.
//
// So the trigger is no longer inferred: the scheduled task passes --hide-console
// (see internal/agent/service/taskxml.go) and a human typing `keld-agent run`
// does not. And the mechanism no longer depends on the count being right:
//
//   - FreeConsole runs UNCONDITIONALLY, and is safe by construction. It detaches
//     THIS process. If nothing else is attached, Windows destroys the console and
//     its window goes with it — which is the scheduled-task case. If a shell IS
//     attached, the console survives and the user's terminal is untouched. There
//     is no input under which it can close a window someone is using.
//   - ShowWindow is now only an OPTIMISATION, taken when the count says we are
//     alone, to kill the window immediately rather than let it flash. If that
//     count is wrong we simply skip it, and FreeConsole still does the real work.
//
// Logs: after detaching, writes to stdout/stderr go nowhere. The Go log package
// discards write errors, so nothing fails. There is no file sink on Windows, so
// the way to watch this daemon is to run it in a terminal yourself WITHOUT the
// flag — which is exactly the path that never detaches.
const swHide = 0

func hideOwnConsole() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	user32 := syscall.NewLazyDLL("user32.dll")

	getConsoleWindow := kernel32.NewProc("GetConsoleWindow")
	getConsoleProcessList := kernel32.NewProc("GetConsoleProcessList")
	freeConsole := kernel32.NewProc("FreeConsole")
	showWindow := user32.NewProc("ShowWindow")

	if hwnd, _, _ := getConsoleWindow.Call(); hwnd != 0 {
		var pids [8]uint32
		if n, _, _ := getConsoleProcessList.Call(
			uintptr(unsafe.Pointer(&pids[0])), uintptr(len(pids)),
		); n == 1 {
			showWindow.Call(hwnd, swHide)
		}
	}
	// Unconditional, and the part that actually guarantees the outcome.
	freeConsole.Call()
}
