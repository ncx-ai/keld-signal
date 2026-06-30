// Package agentcli is the cobra root for the keld-agent binary.
package agentcli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-cli/internal/agent/daemon"
	"github.com/ncx-ai/keld-cli/internal/version"
)

// NewRootCmd builds the keld-agent command tree.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "keld-agent",
		Short:         "Keld enrichment daemon",
		Version:       version.CLI,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(&cobra.Command{
		Use:   "run",
		Short: "Run the enrichment daemon in the foreground.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return daemon.Run(ctx)
		},
	})
	return root
}

// Execute runs the keld-agent CLI and returns an exit code.
func Execute() int {
	if err := NewRootCmd().Execute(); err != nil {
		return 1
	}
	return 0
}
