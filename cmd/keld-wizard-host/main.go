// keld-wizard-host — the two things the Windows installer's wizard page cannot
// do for itself.
//
// Inno Setup's Pascal Script can neither host a browser nor run a child process
// asynchronously while keeping its window responsive. This helper does both, and
// ⚠️ **IT DECIDES NOTHING** — the same rule `installers/macos/plugin/KeldSetup.m`
// follows. Auth, tool detection and path resolution stay in Go where they are
// tested; the wizard page renders the events they already emit.
//
//	--run <exe> --events-dir <dir> [--sentinel <f>] [--parent-pid <n>] -- <args…>
//	    Runs <exe> <args…>, publishing each stdout line as its own numbered event
//	    file, then a final {"event":"__exit","code":N} so the page knows it
//	    finished.
//
//	--clipboard --events-dir <dir>
//	    Publishes one {"event":"clipboard","text":"…"} — Pascal Script cannot read
//	    the clipboard at all.
//
//	--panel <hwnd> --url <url> [--sentinel <f>] [--parent-pid <n>]
//	    Embeds a WebView2 surface in <hwnd> — a TPanel on the wizard page, owned
//	    by another process — and navigates to <url>.
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// exitNoWebView2 is distinct from a generic failure ON PURPOSE: the page falls
// back to opening the URL in the default browser for this case only, and
// treating "something broke" the same way would hide real errors behind a
// browser window nobody expected.
const exitNoWebView2 = 3

type options struct {
	Mode      string // "run" | "panel"
	Exe       string
	Args      []string
	EventsDir string
	Sentinel  string
	ParentPID int
	Panel     uintptr
	URL       string
}

func usage() {
	fmt.Fprint(os.Stderr, `keld-wizard-host

  --run <exe> --events-dir <dir> [--sentinel <f>] [--parent-pid <n>] -- <args...>
  --panel <hwnd> --url <url> [--sentinel <f>] [--parent-pid <n>]
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
		case "--events-dir":
			o.EventsDir, err = next()
		case "--sentinel":
			o.Sentinel, err = next()
		case "--parent-pid":
			var s string
			if s, err = next(); err == nil {
				o.ParentPID, err = strconv.Atoi(s)
			}
		case "--clipboard":
			o.Mode = "clipboard"
		case "--panel":
			o.Mode = "panel"
			var s string
			if s, err = next(); err == nil {
				var h uint64
				h, err = strconv.ParseUint(s, 10, 64)
				o.Panel = uintptr(h)
			}
		case "--url":
			o.URL, err = next()
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
	case "clipboard":
		if o.EventsDir == "" {
			return fmt.Errorf("--clipboard needs --events-dir")
		}
	case "panel":
		if o.Panel == 0 {
			return fmt.Errorf("--panel needs a window handle")
		}
		if o.URL == "" {
			return fmt.Errorf("--panel needs --url")
		}
	default:
		return fmt.Errorf("one of --run, --panel or --clipboard is required")
	}
	return nil
}

// watchForExit ends this process when the wizard says so, or when the wizard is
// gone. ⚠️ BOTH HALVES ARE NEEDED. The sentinel covers Cancel, which the page
// can act on; the parent check covers the wizard being killed, which it cannot.
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
	case "panel":
		os.Exit(panel(o))
	case "clipboard":
		os.Exit(reportClipboard(o))
	}
}
