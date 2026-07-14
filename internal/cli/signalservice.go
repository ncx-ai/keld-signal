package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/agent/service"
)

// newSignalServiceCmds returns start/stop/restart lifecycle controls for the
// keld-agent background service, exposed under `keld signal`. They drive the
// installed per-user service (systemd --user / launchd / schtasks) via the same
// service ops `keld-agent` uses, so either binary can control the daemon.
func newSignalServiceCmds() []*cobra.Command {
	type ctl struct {
		use, short, done string
		run              func() error
	}
	ctls := []ctl{
		{"start", "Start the Keld background agent.", "started", service.Start},
		{"stop", "Stop the Keld background agent.", "stopped", service.Stop},
		{"restart", "Restart the Keld background agent (picks up a new binary).", "restarted", service.Restart},
	}
	cmds := make([]*cobra.Command, 0, len(ctls))
	for _, c := range ctls {
		c := c
		cmds = append(cmds, &cobra.Command{
			Use:   c.use,
			Short: c.short,
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := c.run(); err != nil {
					return err
				}
				fmt.Printf("Keld agent %s.\n", c.done)
				return nil
			},
		})
	}
	return cmds
}
