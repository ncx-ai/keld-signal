package cli

import (
	"fmt"
	"net/url"
	"os/exec"
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
func newSignalOpenCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "open",
		Short: "Open the Keld Signal page in your browser",
		Long: "Open the Keld Signal page: today's focus blocks, your projects, and whether\n" +
			"this machine's work reached Atlas. Served by the local agent on loopback;\n" +
			"nothing is fetched from the internet to render it.",
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
