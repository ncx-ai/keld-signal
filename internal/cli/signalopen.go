package cli

import (
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/errs"
)

// newSignalOpenCmd is `keld signal open`: the Keld Signal page, in the
// browser, on every platform (docs/v3/contracts.md, D6).
//
// ⚠️ **The secret rides ONE query parameter and the page immediately removes it
// from the URL.** The daemon's loopback routes are authenticated with the same
// per-user secret `/enrich` uses, and a browser cannot set a header on a
// top-level navigation — so the first load carries `?secret=`, the page copies
// it into the `keld_secret` cookie and calls `history.replaceState`. Without
// that removal the secret would sit in the address bar, in the back stack and
// in every screenshot anyone sends us while debugging.
//
// ⚠️ **No browser is opened when the daemon is not running.** Loading a page
// that can only say "nothing is here" teaches nobody anything; the terminal can
// say the actionable thing (start it) in one line, and does.
//
// ⚠️ **The desktop app is preferred over the browser when it's installed**
// (app/, lane G2/D9). The app shows the identical page — it opens the same
// loopback URL this command would, from its own `read_agent`/`page_url` in
// `app/src-tauri/src/main.rs` — so which frame renders it is a launcher
// preference, not a second implementation to keep in sync. `--browser` forces
// the old behaviour for anyone who wants the browser tab regardless.
func newSignalOpenCmd() *cobra.Command {
	var printOnly bool
	var forceBrowser bool
	cmd := &cobra.Command{
		Use:   "open",
		Short: "Open the Keld Signal page",
		Long: "Open the Keld Signal page: today's focus blocks, your projects, and whether\n" +
			"this machine's work reached Atlas. Served by the local agent on loopback;\n" +
			"nothing is fetched from the internet to render it.\n\n" +
			"Opens the Keld Signal desktop app when it is installed, otherwise a browser tab.",
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := agentcfg.Read()
			if err != nil || info == nil || info.Port == 0 {
				console.Print("Keld Signal is not running on this machine.")
				console.Print("Start it with: keld-agent run   (or: keld-agent install)")
				return errs.ErrSilentExit
			}
			u := pageURL(info)
			if printOnly {
				fmt.Println(u)
				return nil
			}
			if !forceBrowser {
				if appPath, ok := appInstallPath(); ok {
					if err := launchApp(appPath); err == nil {
						console.Print("Opened the Keld Signal app.")
						return nil
					}
					// Installed but wouldn't launch (moved, quarantined,
					// mid-uninstall) — fall through to the browser rather
					// than leaving the user with nothing.
				}
			}
			if err := openBrowser(u); err != nil {
				// Not a failure worth an exit code: the URL is the whole
				// deliverable and a headless box simply has nowhere to open it.
				console.Print("Could not open a browser. Open this yourself:")
				console.Print("  " + u)
				return nil
			}
			console.Print("Opened " + fmt.Sprintf("http://127.0.0.1:%d/", info.Port))
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "print the URL instead of opening a browser")
	cmd.Flags().BoolVar(&forceBrowser, "browser", false, "open in the browser even when the Keld Signal app is installed")
	return cmd
}

func pageURL(info *agentcfg.Info) string {
	q := url.Values{}
	q.Set("secret", info.Secret)
	return fmt.Sprintf("http://127.0.0.1:%d/?%s", info.Port, q.Encode())
}

// openBrowser is the same three-platform call the device-authorization login
// already makes. It is duplicated rather than shared because auth's version
// deliberately does not wait for the child (some Linux xdg-open setups do not
// return until the browser exits) and this one wants the same behaviour for the
// same reason — sharing would couple a login flow to a CLI convenience.
func openBrowser(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", u)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }() // reap; never block on the browser's lifetime
	return nil
}

// appInstallPath locates an installed Keld Signal desktop app, if any.
//
// This is a filesystem check, never a running-process probe: the app is
// meant to be launched on demand (it holds no daemon connection of its own
// beyond reading agent.json), so "is it running" is the wrong question —
// "is it installed" is.
func appInstallPath() (string, bool) {
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		return firstExisting(darwinAppCandidates(home), true)
	case "windows":
		if p, ok := firstExisting(windowsAppCandidates(os.Getenv), false); ok {
			return p, true
		}
		if p, err := exec.LookPath("Keld Signal.exe"); err == nil {
			return p, true
		}
		return "", false
	default: // linux and anything else exec.Command can launch directly
		home, _ := os.UserHomeDir()
		if p, ok := firstExisting(linuxAppCandidates(home), false); ok {
			return p, true
		}
		if p, err := exec.LookPath("keld-signal"); err == nil {
			return p, true
		}
		return "", false
	}
}

// darwinAppCandidates checks both the machine-wide and the per-user
// Applications directory — a pkg installer typically writes the former, a
// user dragging the .app from a dmg the latter.
func darwinAppCandidates(home string) []string {
	c := []string{"/Applications/Keld Signal.app"}
	if home != "" {
		c = append(c, filepath.Join(home, "Applications", "Keld Signal.app"))
	}
	return c
}

// windowsAppCandidates mirrors the two install locations a Tauri NSIS/WiX
// bundle typically offers: per-user (%LOCALAPPDATA%, no admin needed) and
// machine-wide (%ProgramFiles%).
func windowsAppCandidates(getenv func(string) string) []string {
	var c []string
	if lad := getenv("LOCALAPPDATA"); lad != "" {
		c = append(c, filepath.Join(lad, "Keld Signal", "Keld Signal.exe"))
	}
	if pf := getenv("ProgramFiles"); pf != "" {
		c = append(c, filepath.Join(pf, "Keld Signal", "Keld Signal.exe"))
	}
	return c
}

// linuxAppCandidates checks the conventional system binary dirs plus the
// per-user one — there is no packaged Linux build of the app yet, but a
// hand-built or side-loaded binary would land in one of these.
func linuxAppCandidates(home string) []string {
	c := []string{"/usr/bin/keld-signal", "/usr/local/bin/keld-signal", "/opt/keld-signal/keld-signal"}
	if home != "" {
		c = append(c, filepath.Join(home, ".local", "bin", "keld-signal"))
	}
	return c
}

// firstExisting returns the first candidate that exists on disk, requiring a
// directory (a macOS .app bundle) or a plain file (a Linux/Windows binary) —
// so a same-named file where a bundle is expected, or vice versa, doesn't
// match.
func firstExisting(candidates []string, wantDir bool) (string, bool) {
	for _, c := range candidates {
		info, err := os.Stat(c)
		if err != nil {
			continue
		}
		if info.IsDir() == wantDir {
			return c, true
		}
	}
	return "", false
}

// buildLaunchCmd is the platform-specific launch strategy, split out from
// launchApp so it's testable without actually starting a process.
func buildLaunchCmd(path string) *exec.Cmd {
	if runtime.GOOS == "darwin" {
		// `open -a <path>` launches the bundle the same way Finder would
		// (Launch Services), rather than executing the Mach-O inside it
		// directly — which matters for a .app, since running the inner
		// binary bypasses the bundle's own activation/focus behaviour.
		return exec.Command("open", "-a", path)
	}
	return exec.Command(path)
}

// launchApp starts the installed app and never waits on it — same reaping
// discipline as openBrowser, for the same reason: this process must not
// block on the lifetime of whatever it just opened.
func launchApp(path string) error {
	cmd := buildLaunchCmd(path)
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
