package auth

import (
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/ncx-ai/keld-cli/internal/api"
	"github.com/ncx-ai/keld-cli/internal/console"
	"github.com/ncx-ai/keld-cli/internal/errs"
	"github.com/ncx-ai/keld-cli/internal/paths"
)

// Login performs the OAuth2 device-flow login against the Atlas API.
// sleep and opener are injectable for testing; in production use time.Sleep
// and openURL respectively. The opener is launched concurrently so it can never
// block the device-poll loop.
func Login(c *api.Client, openBrowser bool, sleep func(time.Duration), opener func(string) error) (*AuthData, error) {
	ds, err := c.DeviceStart()
	if err != nil {
		return nil, err
	}

	console.Print(fmt.Sprintf(
		"To authorize this device, open:\n  %s\nThe code %s is already filled in — confirm it matches, then approve.",
		ds.VerificationURL, ds.UserCode,
	))

	if openBrowser {
		console.Print("(Opening your browser…)")
		// Launch the browser concurrently. The opener can block until the browser
		// process exits (some Linux xdg-open setups do not return until the
		// browser window is closed), and the poll loop below MUST start regardless.
		// Best-effort: the URL is printed above for manual use, so a launch
		// failure never aborts login — the result is intentionally ignored.
		go func() { _ = opener(ds.VerificationURL) }()
	}

	waited := 0
	for waited <= ds.ExpiresIn {
		result, err := c.DevicePoll(ds.DeviceCode)
		if err != nil {
			return nil, err
		}
		if result != nil {
			str := func(k string) (string, bool) { s, ok := result[k].(string); return s, ok }
			at, ok1 := str("access_token")
			pr, ok2 := str("principal")
			org, ok3 := str("org")
			if !ok1 || !ok2 || !ok3 {
				return nil, errs.New("Atlas returned an unexpected device-poll response")
			}
			auth := AuthData{
				AccessToken: at,
				Principal:   pr,
				Org:         org,
				APIURL:      c.BaseURL,
			}
			if err := Save(auth); err != nil {
				return nil, err
			}
			console.Print(fmt.Sprintf("Logged in as %s (org: %s)", auth.Principal, auth.Org))
			return &auth, nil
		}
		sleep(time.Duration(ds.Interval) * time.Second)
		interval := ds.Interval
		if interval < 1 {
			interval = 1
		}
		waited += interval
	}

	return nil, errs.New("login timed out; please run `keld login` again")
}

// RequireAuth returns usable auth. When force is false it is lazy: stored creds
// are returned as-is if present (a caller's subsequent API call surfaces any
// staleness). When force is true — the explicit `keld login` command — it always
// runs a fresh device-flow login, replacing any stored creds, so an explicit
// login never silently trusts cached credentials that may have been rotated or
// invalidated server-side. force is ignored when noLogin is set (a fresh login
// needs a browser): it falls back to the lazy path so `keld login --no-login`
// still reports stored presence without opening a browser.
func RequireAuth(noLogin bool, openBrowser bool, force bool) (*AuthData, error) {
	if !(force && !noLogin) {
		existing, err := Load()
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return existing, nil
		}
		if noLogin {
			return nil, errs.New("not logged in (run `keld login`; --no-login was set)")
		}
	}
	return Login(
		api.NewClient(paths.APIBase(), ""),
		openBrowser,
		time.Sleep,
		openURL,
	)
}

// openURL launches the user's default browser pointed at url. It starts the
// launcher without waiting (so it never blocks the caller) and discards the
// browser's stdout/stderr (so GPU/driver chatter — e.g. libEGL warnings — does
// not pollute the terminal). A non-nil error means the launcher failed to start;
// callers treat browser opening as best-effort.
func openURL(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default: // linux, *bsd, etc.
		cmd = exec.Command("xdg-open", url)
	}
	cmd.Stdout = nil // discard → no browser chatter in our terminal
	cmd.Stderr = nil
	return cmd.Start() // Start, not Run: do not wait for the browser to exit
}
