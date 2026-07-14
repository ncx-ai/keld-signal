package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/errs"
	"github.com/ncx-ai/keld-signal/internal/version"
)

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "keld",
		Short:         "Keld CLI",
		Version:       version.CLI,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// Auth commands registered in Task 14.
	root.AddCommand(newLoginCmd())
	root.AddCommand(newLogoutCmd())
	root.AddCommand(newWhoamiCmd())
	// Hidden hook runner: keld __hook --source <tool>
	root.AddCommand(newHookCmd())
	// Signal group is expanded in later tasks.
	signal := &cobra.Command{
		Use:   "signal",
		Short: "Set up Keld Signal telemetry for your local AI coding tools.",
	}
	signal.AddCommand(newSetupCmd())
	signal.AddCommand(newStatusCmd())
	signal.AddCommand(newSignalMetricsCmd())
	signal.AddCommand(newSignalEnrichCmd())
	signal.AddCommand(newDoctorCmd())
	signal.AddCommand(newUninstallCmd())
	signal.AddCommand(newRestoreCmd())
	for _, c := range newSignalServiceCmds() {
		signal.AddCommand(c)
	}
	root.AddCommand(signal)
	return root
}

// Execute runs the CLI and returns a process exit code.
func Execute() int {
	root := NewRootCmd()
	if err := root.Execute(); err != nil {
		if errors.Is(err, errs.ErrSilentExit) {
			// The command already printed its own message; exit non-zero silently.
			return 1
		}
		var ke *errs.Error
		if errors.As(err, &ke) {
			color.New(color.FgRed, color.Bold).Fprint(os.Stderr, "Error: ")
			fmt.Fprintln(os.Stderr, ke.Msg)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		return 1
	}
	return 0
}
