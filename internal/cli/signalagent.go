package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/localagent"
)

// newSignalMetricsCmd builds `keld signal metrics`.
func newSignalMetricsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "metrics",
		Short: "Print the local signal service's GLiNER2 sidecar /metrics JSON.",
		RunE: func(cmd *cobra.Command, args []string) error {
			info, err := agentcfg.Read()
			if err != nil {
				return err
			}
			body, err := localagent.Metrics(info)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), body)
			return nil
		},
	}
}
