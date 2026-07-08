package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/api"
	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/config"
	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/diffview"
	"github.com/ncx-ai/keld-signal/internal/errs"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/tools"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// SetupOpts holds behavioural knobs for runSetup that are separate from the
// telemetry parameters.
type SetupOpts struct {
	DryRun          bool
	Yes             bool
	ShowDiff        bool
	Confirm         func(string) bool
	ResolveConflict func(a tools.Adapter, plan tools.Plan) string // returns "skip"/"replace"/"abort"
	Emit            func(SetupEvent)                              // non-nil ⇒ machine mode: emit events, suppress human output
}

// runSetup applies keld telemetry configuration to each adapter, writes the
// manifest, and returns the resulting Manifest.
func runSetup(adapters []tools.Adapter, p tools.SetupParams, client *api.Client, ob *api.Onboarding, opts SetupOpts) (*config.Manifest, error) {
	quiet := opts.Emit != nil
	emit := func(e SetupEvent) {
		if opts.Emit != nil {
			opts.Emit(e)
		}
	}
	say := func(s string) {
		if !quiet {
			console.Print(s)
		}
	}

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

		if !quiet {
			console.Rule(fmt.Sprintf("%s · %s", adapter.DisplayName(), path))
		}

		plan := adapter.Apply(before, p, false)

		if plan.Conflict != "" {
			say(fmt.Sprintf("  conflict: %s", plan.Conflict))
			if opts.DryRun {
				say("  (dry-run: would be skipped)")
				emit(SetupEvent{Kind: "tool", Name: adapter.Name(), Display: adapter.DisplayName(), Action: "skipped_conflict", Path: path})
				continue
			}
			if opts.Yes {
				say("  skipped (--yes)")
				emit(SetupEvent{Kind: "tool", Name: adapter.Name(), Display: adapter.DisplayName(), Action: "skipped_conflict", Path: path})
				continue
			}
			choice := opts.ResolveConflict(adapter, plan)
			if choice == "abort" {
				say("Aborted.")
				return nil, errs.ErrSilentExit
			}
			if choice == "replace" {
				plan = adapter.Apply(before, p, true)
				if plan.Conflict != "" {
					say(fmt.Sprintf("  can't replace: %s", plan.Conflict))
					say("  skipped")
					emit(SetupEvent{Kind: "tool", Name: adapter.Name(), Display: adapter.DisplayName(), Action: "skipped_conflict", Path: path})
					continue
				}
				if !quiet {
					diffview.Render(before, plan.AfterText, plan.ConfigPath)
					for _, line := range plan.Summary {
						console.Print(fmt.Sprintf("  %s", line))
					}
				}
				approveds = append(approveds, approved{adapter, plan})
				continue
			}
			say("  skipped")
			emit(SetupEvent{Kind: "tool", Name: adapter.Name(), Display: adapter.DisplayName(), Action: "skipped_conflict", Path: path})
			continue
		}

		if !plan.Changed {
			say("  already configured — no changes")
			emit(SetupEvent{Kind: "tool", Name: adapter.Name(), Display: adapter.DisplayName(), Action: "already_configured", Path: path})
			continue
		}

		if !quiet {
			if opts.ShowDiff {
				diffview.Render(before, plan.AfterText, plan.ConfigPath)
			}
			for _, line := range plan.Summary {
				console.Print(fmt.Sprintf("  %s", line))
			}
		}
		approveds = append(approveds, approved{adapter, plan})
	}

	say("\nHook · keld __hook (writes ~/.keld/hook.json)")

	if opts.DryRun {
		say("\n--dry-run: no changes written.")
		return config.LoadManifest()
	}
	if len(approveds) == 0 {
		say("\nNothing to apply.")
		emit(SetupEvent{Kind: "done", Configured: 0, Endpoint: ob.Endpoint})
		return config.LoadManifest()
	}
	if !opts.Yes && !opts.Confirm(fmt.Sprintf("Apply %d change(s)?", len(approveds))) {
		say("Aborted.")
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
			say(fmt.Sprintf("  backed up %s → %s", a.plan.ConfigPath, backup))
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
		say(fmt.Sprintf("  ✓ %s", a.adapter.DisplayName()))
		emit(SetupEvent{Kind: "tool", Name: a.adapter.Name(), Display: a.adapter.DisplayName(), Action: "configured", Path: a.plan.ConfigPath, Backup: backup})
	}

	if err := manifest.Save(); err != nil {
		return nil, err
	}
	say("\nSetup complete. Restart any running sessions to pick up the new config.")
	emit(SetupEvent{Kind: "done", Configured: len(manifest.Tools), Endpoint: ob.Endpoint})
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
	var jsonOut bool

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
				if jsonOut {
					emitEvent(doneEvent{Event: "done", Configured: 0})
				} else {
					console.Print("No supported tools detected. Use --tool to target one explicitly.")
				}
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
			if jsonOut {
				opts.Yes = true
				opts.Emit = func(e SetupEvent) {
					switch e.Kind {
					case "tool":
						emitEvent(toolEvent{Event: "tool", Name: e.Name, Display: e.Display, Action: e.Action, Path: e.Path, Backup: e.Backup})
					case "done":
						emitEvent(doneEvent{Event: "done", Configured: e.Configured, Endpoint: e.Endpoint})
					}
				}
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
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable NDJSON events on stdout (implies --yes).")

	return cmd
}
