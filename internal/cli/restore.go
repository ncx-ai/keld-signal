package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/tools"
)

// runRestore rolls back keld-touched tool configs: a tool with a recorded
// pristine backup (config.BackupConfig's one-time pre-keld copy) is restored
// from it in full; a tool keld configured with no backup (fresh-created
// file) has keld's config surgically stripped via its adapter instead. Either
// way the tool is dropped from the manifest so it no longer sends telemetry
// to keld. names filters targets to those tools if non-nil; yes skips the
// confirmation prompt; dryRun previews the plan without writing anything.
func runRestore(m *config.Manifest, names []string, yes, dryRun bool, confirm func(string) bool) error {
	// Collect targets: all tools in the manifest, filtered by names if given.
	var targets []string
	for name := range m.Tools {
		if names == nil || contains(names, name) {
			targets = append(targets, name)
		}
	}

	if len(targets) == 0 {
		console.Print("Nothing to restore.")
		return nil
	}

	if !dryRun && !yes && !confirm(fmt.Sprintf("Restore %s to their pre-keld config? This overwrites the current config.", strings.Join(targets, ", "))) {
		console.Print("Aborted.")
		return nil
	}

	for _, name := range targets {
		tm := m.Tools[name]

		display := name
		if adapter, err := tools.Get(name); err == nil {
			display = adapter.DisplayName()
		}

		hasBackup := tm.BackupPath != nil
		if hasBackup {
			if _, err := os.Stat(*tm.BackupPath); err != nil {
				hasBackup = false
			}
		}

		if hasBackup {
			if dryRun {
				console.Print(fmt.Sprintf("  would restore %s from %s", display, *tm.BackupPath))
				continue
			}
			if err := config.RestoreBackup(*tm.BackupPath, tm.ConfigPath); err != nil {
				return err
			}
			delete(m.Tools, name)
			console.Print(fmt.Sprintf("  ✓ %s restored from backup", display))
			continue
		}

		if dryRun {
			console.Print(fmt.Sprintf("  would strip keld config from %s (no backup)", display))
			continue
		}

		adapter, err := tools.Get(name)
		if err != nil {
			// Unknown adapter; nothing to strip, but still drop from manifest.
			delete(m.Tools, name)
			console.Print(fmt.Sprintf("  ✓ %s keld config removed (no backup to restore)", display))
			continue
		}
		if err := stripToolConfig(m, name, tm, adapter); err != nil {
			return err
		}
		console.Print(fmt.Sprintf("  ✓ %s keld config removed (no backup to restore)", display))
	}

	if dryRun {
		return nil
	}

	if err := m.Save(); err != nil {
		return err
	}
	console.Print("Done.")
	return nil
}

func newRestoreCmd() *cobra.Command {
	var yes bool
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "restore [tool...]",
		Short: "Restore tool configs from keld's pre-setup backups (or strip keld's config where none exists).",
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := config.LoadManifest()
			if err != nil {
				return err
			}

			var names []string
			if len(args) > 0 {
				names = args
			}

			return runRestore(m, names, yes, dryRun, stdinConfirm)
		},
	}

	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt.")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview the restore without writing anything.")

	return cmd
}
