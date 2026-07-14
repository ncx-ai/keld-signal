package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// runUninstall removes keld telemetry configuration from the listed tools (or
// all tools if names is nil). It updates and saves the manifest.
func runUninstall(m *config.Manifest, names []string, yes bool, confirm func(string) bool) error {
	// Collect targets: all tools in the manifest, filtered by names if given.
	var targets []string
	for name := range m.Tools {
		if names == nil || contains(names, name) {
			targets = append(targets, name)
		}
	}

	if len(targets) == 0 {
		console.Print("Nothing to uninstall.")
		return nil
	}

	if !yes && !confirm(fmt.Sprintf("Remove Keld config from %s?", strings.Join(targets, ", "))) {
		console.Print("Aborted.")
		return nil
	}

	for _, name := range targets {
		tm := m.Tools[name]
		adapter, err := tools.Get(name)
		if err != nil {
			// Unknown adapter; skip but still remove from manifest.
			delete(m.Tools, name)
			continue
		}

		if err := stripToolConfig(m, name, tm, adapter); err != nil {
			return err
		}
		console.Print(fmt.Sprintf("  ✓ %s", adapter.DisplayName()))
	}

	// If no tools remain, clear hook.json, state dir, and manifest fields.
	if len(m.Tools) == 0 {
		hookCfg := paths.HookConfigPath()
		if _, err := os.Stat(hookCfg); err == nil {
			_ = os.Remove(hookCfg)
		}
		_ = os.RemoveAll(paths.StateDir())
		m.Hook = nil
		m.Endpoint = nil
		m.Actor = nil
	}

	if err := m.Save(); err != nil {
		return err
	}
	console.Print("Done.")
	return nil
}

// stripToolConfig surgically removes keld's managed config (env vars + hooks)
// from a tool's config file via its adapter, deleting the file if keld
// created it fresh and it's now empty, cleans up the .keld.bak sibling if
// present, and drops the tool from the manifest. It does not save the
// manifest — callers are responsible for that.
func stripToolConfig(m *config.Manifest, name string, tm config.ToolManifest, adapter tools.Adapter) error {
	var current *string
	if data, err := os.ReadFile(tm.ConfigPath); err == nil {
		s := string(data)
		current = &s
	}

	plan := adapter.Remove(current, tm.Managed)

	created, _ := anyBool(tm.Managed, "created")
	if created {
		deleted, err := config.DeleteIfEmpty(tm.ConfigPath, plan.AfterText)
		if err != nil {
			return err
		}
		if !deleted {
			if err := config.WriteAtomic(tm.ConfigPath, plan.AfterText, false); err != nil {
				return err
			}
		}
	} else {
		if err := config.WriteAtomic(tm.ConfigPath, plan.AfterText, false); err != nil {
			return err
		}
	}

	// Remove the .keld.bak sibling if it exists.
	bak := tm.ConfigPath + ".keld.bak"
	if _, err := os.Stat(bak); err == nil {
		_ = os.Remove(bak)
	}

	delete(m.Tools, name)
	return nil
}

// anyBool extracts a bool from a map[string]any, returning false if absent or
// not a bool.
func anyBool(m map[string]any, key string) (bool, bool) {
	if m == nil {
		return false, false
	}
	v, ok := m[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

// contains reports whether s is in slice.
func contains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}
	return false
}

func newUninstallCmd() *cobra.Command {
	var toolNames []string
	var yes bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove Keld telemetry config and hook.",
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := config.LoadManifest()
			if err != nil {
				return err
			}

			var names []string
			if len(toolNames) > 0 {
				names = toolNames
			}

			return runUninstall(m, names, yes, stdinConfirm)
		},
	}

	cmd.Flags().StringSliceVar(&toolNames, "tool", nil, "Target specific tool(s) by name.")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt.")

	return cmd
}
