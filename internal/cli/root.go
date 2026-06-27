package cli

import (
	"fmt"
	"os"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-cli/internal/errs"
)

func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "keld",
		Short:         "Keld CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	// Auth commands (login/logout/whoami) are implemented fully in Task 14;
	// stubs are registered here so that --help lists them from the start.
	login := &cobra.Command{Use: "login", Short: "Authenticate with Keld.", RunE: func(cmd *cobra.Command, args []string) error { return nil }}
	logout := &cobra.Command{Use: "logout", Short: "Sign out of Keld.", RunE: func(cmd *cobra.Command, args []string) error { return nil }}
	whoami := &cobra.Command{Use: "whoami", Short: "Show the current authenticated user.", RunE: func(cmd *cobra.Command, args []string) error { return nil }}
	// Signal group is expanded in later tasks.
	signal := &cobra.Command{
		Use:   "signal",
		Short: "Set up Keld Signal telemetry for your local AI coding tools.",
	}
	root.AddCommand(login, logout, whoami, signal)
	return root
}

// Execute runs the CLI and returns a process exit code.
func Execute() int {
	root := NewRootCmd()
	if err := root.Execute(); err != nil {
		var ke *errs.Error
		if as(err, &ke) {
			color.New(color.FgRed, color.Bold).Fprint(os.Stderr, "Error: ")
			fmt.Fprintln(os.Stderr, ke.Msg)
		} else {
			fmt.Fprintln(os.Stderr, err)
		}
		return 1
	}
	return 0
}

func as(err error, target **errs.Error) bool {
	for err != nil {
		if e, ok := err.(*errs.Error); ok {
			*target = e
			return true
		}
		type unwrap interface{ Unwrap() error }
		u, ok := err.(unwrap)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
