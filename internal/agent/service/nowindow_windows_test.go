//go:build windows

package service

import (
	"os"
	"strings"
	"testing"
)

// ⚠️ EVERY PROCESS THIS PACKAGE STARTS MUST GO THROUGH `command`.
//
// Each helper here is a console program (schtasks, taskkill). `keld-agent
// install` runs several in a row, and Windows gives every console child of a
// console-less parent a NEW console window and shows it — so the install popped
// a black window per call. Reported on a real install as "a terminal window
// opened and closed twice", on a flow whose entire premise is that no terminal
// ever appears.
//
// Inno's `Flags: runhidden` does not help: it hides keld-agent's own window, and
// keld-agent is not what shows one — its children are.
//
// This has now been fixed in four separate places (the wizard host, this
// package, the daemon's sidecar spawn, and the installer's postinstall action),
// which is exactly why it is worth a test rather than a comment: a bare
// exec.Command here is invisible in review and only shows up as a black
// rectangle on somebody's screen.
func TestEveryProcessGoesThroughTheNoWindowHelper(t *testing.T) {
	b, err := os.ReadFile("service_windows.go")
	if err != nil {
		t.Fatalf("read service_windows.go: %v", err)
	}
	if n := strings.Count(string(b), "exec.Command("); n != 0 {
		t.Errorf("service_windows.go calls exec.Command directly %d time(s); use command() so the child gets CREATE_NO_WINDOW", n)
	}
	if !strings.Contains(string(b), "command(\"schtasks\"") {
		t.Error("nothing starts schtasks through command(); either the task registration is gone or it bypasses the no-window helper")
	}
}
