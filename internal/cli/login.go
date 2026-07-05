package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/console"
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

			// force=true: an explicit `keld login` always re-authenticates rather than
			// trusting stored creds (which may be revoked/rotated). Falls back to the
			// lazy path only under --no-login (no browser available).
			a, err := auth.RequireAuth(noLogin, true, true)
			if err != nil {
				return err
			}
			// Sole "Logged in as …" confirmation (Login() no longer prints it), so the
			// line appears exactly once whether we re-authed or returned stored creds.
			console.Print(fmt.Sprintf("Logged in as %s (org: %s)", a.Principal, a.Org))
			return nil
		},
	}
	cmd.Flags().String("api-url", "", "Target a different Keld API base URL (e.g. http://localhost:8000) for local dev.")
	cmd.Flags().Bool("no-login", false, "Fail instead of opening a browser.")
	return cmd
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
