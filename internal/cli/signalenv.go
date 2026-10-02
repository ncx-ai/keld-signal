package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/agent/service"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/hook"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// restartSignalService restarts the installed background service. A var so a
// test can stand in for it: the real one restarts the developer's own Signal.
var restartSignalService = service.Restart

// newSignalEnvCmd is `keld signal env [prod|dev|local]`: a developer's switch
// for which Atlas this machine uses. It writes `atlas_env` into
// agent-config.json (the installed service carries no environment, so a
// variable could never reach it) and restarts Signal so the daemon picks it
// up. No argument prints the current one.
func newSignalEnvCmd() *cobra.Command {
	var noRestart bool
	cmd := &cobra.Command{
		Use:       "env [prod|dev|local]",
		Short:     "Show or switch which Atlas this machine uses (development).",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"prod", "dev", "local"},
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			if len(args) == 0 {
				printAtlasEnv(out)
				return nil
			}
			if err := settings.WriteAtlasEnv(args[0]); err != nil {
				return err
			}
			printAtlasEnv(out)
			if paths.CurrentAtlasEnv().Name == "custom" {
				fmt.Fprintln(out, "Note: KELD_API_URL / KELD_ATLAS_WEB_URL are set in this shell and win over the setting here; the background service does not see them.")
			}
			if cfg, err := hook.LoadConfig(); err == nil && cfg != nil && cfg.Endpoint != "" && cfg.IngestToken != "" {
				fmt.Fprintf(out, "Still paired, and sending to %s. To pair with this Atlas, Unpair in Settings and sign in again.\n", cfg.Endpoint)
			}
			if noRestart {
				fmt.Fprintln(out, "Restart Signal to use it: keld signal restart")
				return nil
			}
			if err := restartSignalService(); err != nil {
				fmt.Fprintf(out, "Saved. Signal did not restart (%v); restart it to use the new Atlas: keld signal restart\n", err)
				return nil
			}
			fmt.Fprintln(out, "Signal restarted.")
			return nil
		},
	}
	cmd.Flags().BoolVar(&noRestart, "no-restart", false, "save the setting without restarting Signal")
	return cmd
}

func printAtlasEnv(out io.Writer) {
	e := paths.CurrentAtlasEnv()
	if e.API == e.Web {
		fmt.Fprintf(out, "Atlas: %s (%s)\n", e.Name, e.API)
		return
	}
	fmt.Fprintf(out, "Atlas: %s (API %s, web %s)\n", e.Name, e.API, e.Web)
}

// atlasEnvNote is the one line status and doctor print when this machine is
// not on production, or "" when it is.
func atlasEnvNote() string {
	e := paths.CurrentAtlasEnv()
	if e.Name == paths.AtlasEnvs[0].Name {
		return ""
	}
	where := e.API
	if e.Web != e.API {
		where = "API " + e.API + ", web " + e.Web
	}
	return fmt.Sprintf("Atlas: %s (%s), not production. `keld signal env prod` switches back.", e.Name, where)
}
