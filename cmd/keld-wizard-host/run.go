package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
)

// relay runs the child and publishes each stdout line as its own event file.
//
// ⚠️ **EVENTS MUST APPEAR AS THEY ARRIVE, NOT ON EXIT.** The wizard page watches
// the directory to move a status label and to raise the approval panel the moment
// a `device_code` event lands. A relay that published only when the child exited
// would leave the page frozen for the whole of a device flow — minutes — with
// nothing on screen to explain why.
func relay(o options) int {
	em, err := newEmitter(o.EventsDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, "keld-wizard-host:", err)
		return 2
	}

	cmd := exec.Command(o.Exe, o.Args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		em.emitValue(exitEvent{Event: "__exit", Code: 2, Message: err.Error()})
		return 2
	}
	// The page never sees stderr, which is what lets it treat every event file it
	// reads as an event rather than having to tell prose from JSON.
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		em.emitValue(exitEvent{
			Event:   "__exit",
			Code:    2,
			Message: fmt.Sprintf("could not start %s: %v", o.Exe, err),
		})
		return 2
	}

	watchForExit(o.Sentinel, o.ParentPID, func() {
		// Exiting closes the job object, which reaps the child with us.
		os.Exit(1)
	})

	sc := bufio.NewScanner(stdout)
	// keld's events are small, but a long error message must not arrive as a
	// truncated line the page then fails to parse.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		if line := sc.Bytes(); len(line) > 0 {
			em.emit(append([]byte{}, line...))
		}
	}

	// ⚠️ Wait AFTER draining to EOF, never alongside it. The macOS pane shipped a
	// bug where process exit and pipe drain were two signals on two queues, and
	// the last line — `authorized` — could be delivered after the exit had
	// already been reported, and dropped. Draining first makes that ordering
	// structural instead of hopeful.
	code := 0
	if err := cmd.Wait(); err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			code = 2
		}
	}
	em.emitValue(exitEvent{Event: "__exit", Code: code})
	return code
}
