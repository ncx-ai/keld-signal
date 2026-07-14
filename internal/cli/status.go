package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/service"
	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/errs"
	"github.com/ncx-ai/keld-signal/internal/localagent"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// statusRow pairs a tool's display name with its computed status.
type statusRow struct {
	displayName string
	status      tools.ToolStatus
}

// collectStatus mirrors Python's _collect_status: for every adapter it ALWAYS
// reads the adapter's real config file (missing → nil), supplies the manifest's
// managed map when the tool is recorded (else nil), and returns the status rows.
func collectStatus(adapters []tools.Adapter, manifest *config.Manifest) []statusRow {
	rows := make([]statusRow, 0, len(adapters))
	for _, adapter := range adapters {
		var current *string
		if data, err := os.ReadFile(adapter.ConfigPath()); err == nil {
			s := string(data)
			current = &s
		}
		var managed map[string]any
		if tm, inManifest := manifest.Tools[adapter.Name()]; inManifest {
			managed = tm.Managed
		}
		rows = append(rows, statusRow{
			displayName: adapter.DisplayName(),
			status:      adapter.Status(current, managed),
		})
	}
	return rows
}

func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show Keld Signal configuration status.",
		RunE: func(cmd *cobra.Command, args []string) error {
			a, err := auth.Load()
			if err != nil {
				return err
			}
			if a == nil {
				console.Print("Not logged in (run `keld login`)")
			} else {
				console.Print(fmt.Sprintf("Logged in: %s · org %s · %s", a.Principal, a.Org, a.APIURL))
			}

			manifest, err := config.LoadManifest()
			if err != nil {
				return err
			}

			for _, row := range collectStatus(tools.All(), manifest) {
				var state string
				switch {
				case row.status.Configured:
					state = "configured"
				case row.status.Installed:
					state = "not configured"
				default:
					state = "not installed"
				}
				console.Print(fmt.Sprintf("  %-14s %s", row.displayName, state))
			}

			if manifest.Hook != nil {
				console.Print(fmt.Sprintf("  hook            v%s", manifest.Hook.Version))
			}

			info, _ := agentcfg.Read()
			health := localagent.Health(info, service.Status, localagent.FetchText)
			for _, line := range renderLocalService(health) {
				console.Print(line)
			}

			if required, _ := paths.ReauthRequired(); required {
				console.Print(reauthRequiredLine)
			}

			return nil
		},
	}
}

// reauthRequiredLine is the human line surfaced by `keld signal status`,
// `keld signal doctor`, and `keld-agent status` when the daemon's local
// re-authentication marker (paths.ReauthMarkerPath) is present — the CLI
// token itself is gone/revoked and the daemon self-heal can't recover it.
// Kept as one constant so the wording stays identical across all three.
const reauthRequiredLine = "⚠ re-authentication required — run 'keld login', then 'keld-agent restart'"

func newDoctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check Keld Signal configuration for problems.",
		RunE: func(cmd *cobra.Command, args []string) error {
			var problems []string

			manifest, err := config.LoadManifest()
			if err != nil {
				return err
			}

			for name, tm := range manifest.Tools {
				adapter, err := tools.Get(name)
				if err != nil {
					// Unknown tool in manifest — skip silently (matches Python behaviour).
					continue
				}
				var current *string
				if data, err := os.ReadFile(tm.ConfigPath); err == nil {
					s := string(data)
					current = &s
				}
				st := adapter.Status(current, tm.Managed)
				if !st.Configured {
					problems = append(problems,
						fmt.Sprintf("%s: manifest records setup but config is not configured (drift). Re-run `keld setup`.", adapter.DisplayName()),
					)
				}
			}

			if manifest.Hook != nil {
				if _, err := os.Stat(paths.HookConfigPath()); os.IsNotExist(err) {
					problems = append(problems, "hook config (~/.keld/hook.json) is missing. Re-run `keld signal setup`.")
				}
			}

			reauthRequired, _ := paths.ReauthRequired()

			if len(problems) > 0 {
				for _, p := range problems {
					console.Print(fmt.Sprintf("  ✗ %s", p))
				}
			}
			if reauthRequired {
				console.Print("  " + reauthRequiredLine)
			} else {
				// Absent marker means only that the daemon hasn't detected a revoked
				// CLI token — not that login state was verified here. Don't overclaim.
				console.Print("  ✓ no re-authentication required")
			}

			if len(problems) > 0 || reauthRequired {
				return errs.ErrSilentExit
			}
			console.Print("No problems found.")
			return nil
		},
	}
}

// renderLocalService formats the local signal service section of `keld signal
// status` from a Health snapshot. Best-effort: lines are omitted when their
// data is unavailable.
func renderLocalService(h localagent.HealthInfo) []string {
	lines := []string{"Local signal service:",
		fmt.Sprintf("  %-11s %s", "service", h.Service)}
	if !h.DaemonUp {
		return append(lines, fmt.Sprintf("  %-11s %s", "daemon", "not running"))
	}
	lines = append(lines, fmt.Sprintf("  %-11s %s", "daemon", "reachable"))
	if h.Backend != "" {
		backend := h.Backend
		if h.ModelState != "" {
			backend += " · " + h.ModelState
		}
		lines = append(lines, fmt.Sprintf("  %-11s %s", "backend", backend))
	}
	if h.MetricsOK {
		lines = append(lines, fmt.Sprintf("  %-11s rss %.0f MB (model %.0f)", "memory", h.RSSMB, h.ModelCostMB))
	}
	return lines
}
