package auth

import (
	"fmt"
	"os/exec"
	"runtime"
	"time"

	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/errs"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// Login performs the OAuth2 device-flow login against the Atlas API.
// sleep and opener are injectable for testing; in production use time.Sleep
// and openURL respectively. The opener is launched concurrently so it can never
// block the device-poll loop.
func Login(c *api.Client, openBrowser bool, sleep func(time.Duration), opener func(string) error, onStart func(*api.DeviceStart)) (*AuthData, error) {
	ds, err := c.DeviceStart()
	if err != nil {
		return nil, err
	}

	if onStart != nil {
		onStart(ds)
	}

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
			// The "Logged in as …" confirmation is printed by the command layer
			// (login.go) so it appears exactly once regardless of entry path.
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
// defaultDeviceReport prints the human device-code instructions (the pre-seam
// behavior moved out of Login so a --json caller can substitute a JSON emitter).
func defaultDeviceReport(ds *api.DeviceStart) {
	console.Print(fmt.Sprintf(
		"To authorize this device, open:\n  %s\nThe code %s is already filled in — confirm it matches, then approve.",
		ds.VerificationURL, ds.UserCode,
	))
}

func RequireAuth(noLogin bool, openBrowser bool, force bool) (*AuthData, error) {
	return RequireAuthReport(noLogin, openBrowser, force, defaultDeviceReport)
}

// RequireAuthReport is RequireAuth with an injectable device-code reporter, used
// by the machine-readable (--json) login path.
func RequireAuthReport(noLogin bool, openBrowser bool, force bool, onStart func(*api.DeviceStart)) (*AuthData, error) {
	existing, err := Load()
	if err != nil {
		return nil, err
	}
	// A stored token is only valid at the server it was minted on (its APIURL).
	// Unless an explicit --api-url flag already set an override, target that server
	// for every subsequent command — and for a plain re-login — instead of the
	// built-in default. Without this, `keld signal setup` sends a local token to
	// atlas.keld.co and gets 401 "invalid CLI token".
	if existing != nil && existing.APIURL != "" && paths.APIBaseOverride() == "" {
		paths.SetAPIBaseOverride(existing.APIURL)
	}
	if !(force && !noLogin) {
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
		onStart,
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
