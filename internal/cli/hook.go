package cli

import (
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/hook"
)

func newHookCmd() *cobra.Command {
	var source string

	cmd := &cobra.Command{
		Use:    "__hook",
		Short:  "Internal: run the keld telemetry hook (invoked by tool hooks).",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			code := hook.Run(source, os.Stdin, os.Stderr, time.Now())
			os.Exit(code)
			return nil // unreachable
		},
	}

	cmd.Flags().StringVar(&source, "source", "unknown", "Telemetry source identifier (e.g. claude_code, codex).")

	return cmd
}
