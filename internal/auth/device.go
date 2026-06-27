package auth

import (
	"fmt"
	"time"

	"github.com/pkg/browser"

	"github.com/ncx-ai/keld-cli/internal/api"
	"github.com/ncx-ai/keld-cli/internal/console"
	"github.com/ncx-ai/keld-cli/internal/errs"
	"github.com/ncx-ai/keld-cli/internal/paths"
)

// Login performs the OAuth2 device-flow login against the Atlas API.
// sleep and opener are injectable for testing; in production use time.Sleep
// and browser.OpenURL respectively.
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
		if err := opener(ds.VerificationURL); err != nil {
			return nil, err
		}
	}

	waited := 0
	for waited <= ds.ExpiresIn {
		result, err := c.DevicePoll(ds.DeviceCode)
		if err != nil {
			return nil, err
		}
		if result != nil {
			auth := AuthData{
				AccessToken: result["access_token"].(string),
				Principal:   result["principal"].(string),
				Org:         result["org"].(string),
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

// RequireAuth returns the stored auth if present. If no auth is stored and
// noLogin is true it returns an error. Otherwise it runs the device-flow login.
func RequireAuth(noLogin bool, openBrowser bool) (*AuthData, error) {
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
	return Login(
		api.NewClient(paths.APIBase(), ""),
		openBrowser,
		time.Sleep,
		func(url string) error { return browser.OpenURL(url) },
	)
}
