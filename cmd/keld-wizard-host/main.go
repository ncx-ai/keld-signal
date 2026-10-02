// keld-wizard-host — runs a Windows console program with NO console window.
//
// Inno Setup's Pascal Script cannot pass CREATE_NO_WINDOW, and Task Scheduler
// gives a console binary a console window before any of our code runs. This
// helper is built -H windowsgui, so it has no console to hand down, and starts
// its child with CREATE_NO_WINDOW. ⚠️ **IT DECIDES NOTHING**: it launches what it
// is told and reports what happened.
//
//	--run <exe> --events-dir <dir> [--sentinel <f>] [--parent-pid <n>] -- <args…>
//	    Runs <exe> <args…> and waits, publishing each stdout line as its own
//	    numbered event file, then a final {"event":"__exit","code":N}. The
//	    installer's RunQuiet registers the agent this way (keld-agent.iss).
//
//	--spawn <exe> -- <args…>
//	    Starts one long-lived program hidden and waits for it, forwarding its
//	    exit code. The KeldAgent logon task starts the daemon this way
//	    (internal/agent/service/taskxml.go).
//
// ⚠️ `--panel` (an embedded WebView2 sign-in) and `--clipboard` (reading a
// pasted setup code) were REMOVED on 2026-09-29 with the installer's "Set up
// Keld" page: no installer asks anything about Keld any more
// (docs/superpowers/specs/2026-09-29-signal-web-signin-discovery.html, AC-10).
// They are unknown arguments now, and a test pins that.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type options struct {
	Mode      string // "run" | "spawn"
	Exe       string
	Args      []string
	EventsDir string
	Sentinel  string
	ParentPID int
}

func usage() {
	fmt.Fprint(os.Stderr, `keld-wizard-host

  --run <exe> --events-dir <dir> [--sentinel <f>] [--parent-pid <n>] -- <args...>
  --spawn <exe> -- <args...>
`)
}

func parseArgs(argv []string) (options, error) {
	var o options
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		next := func() (string, error) {
			if i+1 >= len(argv) {
				return "", fmt.Errorf("%s needs a value", a)
			}
			i++
			return argv[i], nil
		}
		var err error
		switch a {
		case "--":
			// Everything after `--` belongs to the child, untouched — a keld
			// argument that happens to look like one of ours must not be eaten.
			o.Args = append(o.Args, argv[i+1:]...)
			return o, nil
		case "--run":
			o.Mode = "run"
			o.Exe, err = next()
		case "--spawn":
			o.Mode = "spawn"
			o.Exe, err = next()
		case "--events-dir":
			o.EventsDir, err = next()
		case "--sentinel":
			o.Sentinel, err = next()
		case "--parent-pid":
			var s string
			if s, err = next(); err == nil {
				o.ParentPID, err = strconv.Atoi(s)
			}
		default:
			return o, fmt.Errorf("unknown argument %q", a)
		}
		if err != nil {
			return o, err
		}
	}
	return o, nil
}

func validate(o options) error {
	switch o.Mode {
	case "run":
		if o.Exe == "" {
			return fmt.Errorf("--run needs an executable")
		}
		if o.EventsDir == "" {
			return fmt.Errorf("--run needs --events-dir")
		}
	case "spawn":
		if o.Exe == "" {
			return fmt.Errorf("--spawn needs an executable")
		}
	default:
		return fmt.Errorf("one of --run or --spawn is required")
	}
	return nil
}

// watchForExit ends this process when the caller says so (--sentinel), or when
// the caller is gone (--parent-pid). ⚠️ BOTH HALVES ARE NEEDED. The sentinel
// covers a cancel the caller can act on; the parent check covers the caller
// being killed, which it cannot.
// Either way this process exiting closes the job object, which reaps any child —
// see kill_windows.go for why that is the whole cancellation story.
func watchForExit(sentinel string, parentPID int, onExit func()) {
	if sentinel == "" && parentPID == 0 {
		return
	}
	go func() {
		for {
			time.Sleep(250 * time.Millisecond)
			if sentinel != "" {
				if _, err := os.Stat(sentinel); err == nil {
					onExit()
					return
				}
			}
			if parentPID != 0 && !processAlive(parentPID) {
				onExit()
				return
			}
		}
	}()
}

func main() {
	o, err := parseArgs(os.Args[1:])
	if err == nil {
		err = validate(o)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "keld-wizard-host:", err)
		usage()
		os.Exit(2)
	}

	// Everything this process starts joins a job that dies with it.
	limitChildrenToOurLifetime()

	switch o.Mode {
	case "run":
		os.Exit(relay(o))
	case "spawn":
		os.Exit(spawnHidden(o))
	}
}
