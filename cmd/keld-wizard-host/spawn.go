package main

import (
	"fmt"
	"os"
	"os/exec"
)

// spawnHidden starts one long-lived console program with NO console window and
// waits for it, forwarding its exit code.
//
// ⚠️ **THIS EXISTS FOR TASK SCHEDULER, AND IT IS THE ONE CONSOLE NOTHING ELSE
// COULD SUPPRESS.** The daemon's scheduled task runs with
// `<LogonType>InteractiveToken</LogonType>`, so TASK SCHEDULER creates the
// process — interactively — and Windows gives a console binary a console
// WINDOW before a single line of our code runs. CREATE_NO_WINDOW cannot help,
// because we are not the one calling CreateProcess. `keld-agent run
// --hide-console` then detaches with FreeConsole, and that teardown IS the
// flicker people see: measured on a real machine, and by the daemon's own
// design note, at EVERY LOGON rather than only at install.
//
// This helper is built `-H windowsgui`, so it has no console to hand down, and
// it starts the daemon with CREATE_NO_WINDOW — so no console is ever created.
// The task points at this instead of at keld-agent directly.
//
// ⚠️ IT MUST WAIT, NOT EXIT. Task Scheduler treats the process it launched as
// the task: returning immediately would mark the task finished while the daemon
// ran on unmanaged, and `schtasks /End` would then have nothing to stop. Waiting
// also makes the kill path correct — this process is in a kill-on-close job
// (see limitChildrenToOurLifetime) that the child inherits, so ending the task
// ends the daemon with it.
//
// ⚠️ NO EVENT FILES HERE, WHICH IS WHY `--run` COULD NOT BE REUSED. That mode
// writes one file per line of the child's stdout — right for a wizard step that
// emits a handful of NDJSON events, catastrophic for a daemon that logs for
// days. Nothing is captured: the daemon's stdout goes nowhere, exactly as it
// does today once it has detached from its console.
func spawnHidden(o options) int {
	cmd := exec.Command(o.Exe, o.Args...)
	hideChildWindow(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "keld-wizard-host: spawn:", err)
		return 2
	}
	if err := cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "keld-wizard-host: wait:", err)
		return 2
	}
	return 0
}
