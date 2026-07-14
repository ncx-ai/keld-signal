package cli

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/errs"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

func newLoginCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Authenticate to Keld.",
		RunE: func(cmd *cobra.Command, args []string) error {
			apiURL, _ := cmd.Flags().GetString("api-url")
			noLogin, _ := cmd.Flags().GetBool("no-login")

			if apiURL != "" {
				paths.SetAPIBaseOverride(apiURL)
			}

			jsonOut, _ := cmd.Flags().GetBool("json")
			noBrowser, _ := cmd.Flags().GetBool("no-browser")

			code, _ := cmd.Flags().GetString("code")
			if code != "" {
				if !jsonOut {
					console.Print("")
					console.Print("Signing in…")
				}
				a, err := auth.LoginWithCode(api.NewClient(paths.APIBase(), ""), code)
				if err != nil {
					if jsonOut {
						emitEvent(errorEvent{Event: "error", Message: cleanErrorMessage(err)})
						return errs.ErrSilentExit
					}
					return err
				}
				if jsonOut {
					emitEvent(authorizedEvent{Event: "authorized", Principal: a.Principal, Org: a.Org})
				} else {
					console.Print(fmt.Sprintf("  ✓ %s · org %s", a.Principal, a.Org))
				}
				return nil
			}

			if jsonOut {
				onStart := func(ds *api.DeviceStart) {
					emitEvent(deviceCodeEvent{
						Event:           "device_code",
						VerificationURL: ds.VerificationURL,
						UserCode:        ds.UserCode,
						ExpiresIn:       ds.ExpiresIn,
						Interval:        ds.Interval,
					})
				}
				a, err := auth.RequireAuthReport(noLogin, !noBrowser, true, onStart)
				if err != nil {
					emitEvent(errorEvent{Event: "error", Message: cleanErrorMessage(err)})
					return errs.ErrSilentExit
				}
				emitEvent(authorizedEvent{Event: "authorized", Principal: a.Principal, Org: a.Org})
				return nil
			}

			// force=true: an explicit `keld login` always re-authenticates rather than
			// trusting stored creds (which may be revoked/rotated). Falls back to the
			// lazy path only under --no-login (no browser available).
			console.Print("")
			console.Print("Signing in…")
			a, err := auth.RequireAuth(noLogin, true, true)
			if err != nil {
				return err
			}
			// Sole "✓ <principal> · org <org>" confirmation (Login() no longer prints
			// it), so the line appears exactly once whether we re-authed or returned
			// stored creds.
			console.Print(fmt.Sprintf("  ✓ %s · org %s", a.Principal, a.Org))
			return nil
		},
	}
	cmd.Flags().String("api-url", "", "Target a different Keld API base URL (e.g. http://localhost:8000) for local dev.")
	cmd.Flags().String("code", "", "Redeem a one-time setup code (non-interactive; skips the browser login).")
	cmd.Flags().Bool("no-login", false, "Fail instead of opening a browser.")
	cmd.Flags().Bool("json", false, "Emit machine-readable NDJSON events on stdout (for installer/automation).")
	cmd.Flags().Bool("no-browser", false, "Do not auto-open the browser (the caller opens the verification URL itself).")
	return cmd
}

// cleanErrorMessage returns the clean *errs.Error message when err wraps one
// (mirroring root.go's Execute), falling back to err.Error() otherwise. This
// keeps machine-readable --json error events a single clean line rather than
// leaking an embedded implementation detail — e.g. checkStatus can return
// errors.Join(*errs.Error, *retry.StatusError), whose Error() joins both with
// a newline.
func cleanErrorMessage(err error) string {
	msg := err.Error()
	var ke *errs.Error
	if errors.As(err, &ke) {
		msg = ke.Msg
	}
	return msg
}

func newLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Remove stored credentials.",
		RunE: func(cmd *cobra.Command, args []string) error {
			removed, err := auth.Clear()
			if err != nil {
				return err
			}
			if removed {
				console.Print("Logged out.")
			} else {
				console.Print("Not logged in.")
			}
			return nil
		},
	}
}

func newWhoamiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "whoami",
		Short: "Show the logged-in principal.",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := auth.Load()
			if err != nil {
				return err
			}
			if a == nil {
				return console.Fail("not logged in (run `keld login`)")
			}
			line := fmt.Sprintf("%s · org %s · %s", a.Principal, a.Org, a.APIURL)
			m, err := config.LoadManifest()
			if err == nil && m.Endpoint != nil && *m.Endpoint != "" {
				line += fmt.Sprintf(" · endpoint %s", *m.Endpoint)
			}
			console.Print(line)
			return nil
		},
	}
}
