package agentcli

import (
	"fmt"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/localagent"
	"github.com/spf13/cobra"
)

// newMetricsCmd builds `keld-agent metrics`: print the running GLiNER2
// sidecar's /metrics JSON to stdout.
func newMetricsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "metrics",
		Short: "Print the running GLiNER2 sidecar's /metrics JSON to stdout.",
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
