package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-cli/internal/api"
	"github.com/ncx-ai/keld-cli/internal/auth"
	"github.com/ncx-ai/keld-cli/internal/config"
	"github.com/ncx-ai/keld-cli/internal/console"
	"github.com/ncx-ai/keld-cli/internal/diffview"
	"github.com/ncx-ai/keld-cli/internal/errs"
	"github.com/ncx-ai/keld-cli/internal/paths"
	"github.com/ncx-ai/keld-cli/internal/tools"
	"github.com/ncx-ai/keld-cli/internal/version"
)

// SetupOpts holds behavioural knobs for runSetup that are separate from the
// telemetry parameters.
type SetupOpts struct {
	DryRun          bool
	Yes             bool
	ShowDiff        bool
	Confirm         func(string) bool
	ResolveConflict func(a tools.Adapter, plan tools.Plan) string // returns "skip"/"replace"/"abort"
}

// runSetup applies keld telemetry configuration to each adapter, writes the
// manifest, and returns the resulting Manifest.
func runSetup(adapters []tools.Adapter, p tools.SetupParams, client *api.Client, ob *api.Onboarding, opts SetupOpts) (*config.Manifest, error) {
	type approved struct {
		adapter tools.Adapter
		plan    tools.Plan
	}
	var approveds []approved

	for _, adapter := range adapters {
		path := adapter.ConfigPath()
		var before *string
		if _, err := os.Stat(path); err == nil {
			data, err := os.ReadFile(path)
			if err == nil {
				s := string(data)
				before = &s
			}
		}

		console.Rule(fmt.Sprintf("%s · %s", adapter.DisplayName(), path))

		plan := adapter.Apply(before, p, false)

		if plan.Conflict != "" {
			console.Print(fmt.Sprintf("  conflict: %s", plan.Conflict))
			if opts.DryRun {
				console.Print("  (dry-run: would be skipped)")
				continue
			}
			if opts.Yes {
				console.Print("  skipped (--yes)")
				continue
			}
			choice := opts.ResolveConflict(adapter, plan)
			if choice == "abort" {
				console.Print("Aborted.")
				return nil, errs.ErrSilentExit
			}
			if choice == "replace" {
				plan = adapter.Apply(before, p, true)
				if plan.Conflict != "" {
					console.Print(fmt.Sprintf("  can't replace: %s", plan.Conflict))
					console.Print("  skipped")
					continue
				}
				diffview.Render(before, plan.AfterText, plan.ConfigPath)
				for _, line := range plan.Summary {
					console.Print(fmt.Sprintf("  %s", line))
				}
				approveds = append(approveds, approved{adapter, plan})
				continue
			}
			console.Print("  skipped")
			continue
		}

		if !plan.Changed {
			console.Print("  already configured — no changes")
			continue
		}

		if opts.ShowDiff {
			diffview.Render(before, plan.AfterText, plan.ConfigPath)
		}
		for _, line := range plan.Summary {
			console.Print(fmt.Sprintf("  %s", line))
		}
		approveds = append(approveds, approved{adapter, plan})
	}

	console.Print("\nHook · keld __hook (writes ~/.keld/hook.json)")

	if opts.DryRun {
		console.Print("\n--dry-run: no changes written.")
		return config.LoadManifest()
	}
	if len(approveds) == 0 {
		console.Print("\nNothing to apply.")
		return config.LoadManifest()
	}
	if !opts.Yes && !opts.Confirm(fmt.Sprintf("Apply %d change(s)?", len(approveds))) {
		console.Print("Aborted.")
		return config.LoadManifest()
	}

	endpoint := ob.Endpoint
	actor := ob.Actor
	manifest := &config.Manifest{
		Endpoint: &endpoint,
		Actor:    &actor,
		Tools:    map[string]config.ToolManifest{},
	}
	manifest.Hook = &config.HookRecord{Version: version.CLI}
	if err := config.SaveHookConfig(ob.Endpoint, ob.IngestToken); err != nil {
		return nil, err
	}

	for _, a := range approveds {
		backup, err := config.BackupConfig(a.plan.ConfigPath, a.adapter.Name())
		if err != nil {
			return nil, err
		}
		if backup != "" {
			console.Print(fmt.Sprintf("  backed up %s → %s", a.plan.ConfigPath, backup))
		}
		if err := config.WriteAtomic(a.plan.ConfigPath, a.plan.AfterText, false); err != nil {
			return nil, err
		}
		var backupPtr *string
		if backup != "" {
			backupPtr = &backup
		}
		manifest.Tools[a.adapter.Name()] = config.ToolManifest{
			Name:       a.adapter.Name(),
			ConfigPath: a.plan.ConfigPath,
			Managed:    a.plan.Managed,
			BackupPath: backupPtr,
		}
		console.Print(fmt.Sprintf("  ✓ %s", a.adapter.DisplayName()))
	}

	if err := manifest.Save(); err != nil {
		return nil, err
	}
	console.Print("\nSetup complete. Restart any running sessions to pick up the new config.")
	return manifest, nil
}

// stdinConfirm prompts the user with a [Y/n] prompt and reads their answer. Defaults to yes:
// an empty response (just pressing Enter) confirms; only an explicit "n"/"no" declines.
func stdinConfirm(prompt string) bool {
	fmt.Fprintf(console.Out, "%s [Y/n] ", prompt)
	var resp string
	fmt.Fscanln(os.Stdin, &resp)
	resp = strings.ToLower(strings.TrimSpace(resp))
	return resp != "n" && resp != "no"
}

// stdinResolveConflict prompts the user to skip, replace, or abort for a conflict.
func stdinResolveConflict(a tools.Adapter, plan tools.Plan) string {
	fmt.Fprintf(console.Out, "%s: [s]kip this tool, [r]eplace the conflicting section, or [a]bort everything? [s] ", a.DisplayName())
	var resp string
	fmt.Fscanln(os.Stdin, &resp)
	resp = strings.ToLower(strings.TrimSpace(resp))
	if len(resp) > 0 {
		switch resp[0] {
		case 's':
			return "skip"
		case 'r':
			return "replace"
		case 'a':
			return "abort"
		}
	}
	return "skip"
}

func newSetupCmd() *cobra.Command {
	var toolNames []string
	var dryRun bool
	var showDiff bool
	var yes bool
	var noLogin bool
	var apiURL string

	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Configure detected tools for Keld telemetry.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if apiURL != "" {
				paths.SetAPIBaseOverride(apiURL)
			}

			// force=false: setup is lazy — reuse stored creds when present (it does not
			// force a browser re-login just to configure telemetry).
			a, err := auth.RequireAuth(noLogin, true, false)
			if err != nil {
				return err
			}

			client := api.NewClient(paths.APIBase(), a.AccessToken)
			ob, err := client.Onboarding()
			if err != nil {
				return err
			}

			adapters, err := tools.Select(toolNames)
			if err != nil {
				return err
			}
			if len(adapters) == 0 {
				console.Print("No supported tools detected. Use --tool to target one explicitly.")
				return nil
			}

			p := tools.SetupParams{
				Endpoint:    ob.Endpoint,
				IngestToken: ob.IngestToken,
				Actor:       ob.Actor,
			}

			opts := SetupOpts{
				DryRun:          dryRun,
				Yes:             yes,
				ShowDiff:        showDiff,
				Confirm:         stdinConfirm,
				ResolveConflict: stdinResolveConflict,
			}

			_, err = runSetup(adapters, p, client, ob, opts)
			return err
		},
	}

	cmd.Flags().StringSliceVar(&toolNames, "tool", nil, "Target specific tool(s) by name (e.g. claude_code, codex, gemini).")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Preview changes without writing anything.")
	cmd.Flags().BoolVar(&showDiff, "diff", false, "Show a diff of each config change.")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompts.")
	cmd.Flags().BoolVar(&noLogin, "no-login", false, "Fail instead of opening a browser.")
	cmd.Flags().StringVar(&apiURL, "api-url", "", "Target a different Keld API base URL for local dev.")

	return cmd
}
