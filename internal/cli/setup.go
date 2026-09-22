package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/agent/settings"
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

// keldBinaryPath returns the absolute, symlink-resolved path of the running keld
// binary, for pinning into tool hook commands so a different keld earlier on
// PATH can't hijack them. Falls back to os.Executable's raw value, or "" (bare
// "keld") if even that can't be resolved.
func keldBinaryPath() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		return resolved
	}
	return exe
}

// resolveSetupBinPath picks the keld path pinned into tool hook commands.
//
// ⚠️ IT EXISTS BECAUSE THE macOS INSTALLER RUNS A COPY OF keld FROM INSIDE THE
// WIZARD PLUGIN BUNDLE, at a path that ceases to exist when the wizard closes.
// keldBinaryPath() would pin that temporary path into every tool's hook command
// and the failure would be silent: the config looks right, the hook never runs.
// The installer passes --bin-path /usr/local/keld/keld, which is where the pkg
// actually puts the binary.
func resolveSetupBinPath(flagValue string) string {
	if flagValue != "" {
		return flagValue
	}
	return keldBinaryPath()
}

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
// adoptOnboarding persists the credential this run VERIFIED: hook.json, which
// the daemon reads to decide where to publish, and the manifest's record of
// which CLI wrote it.
//
// It is called from both post-dry-run paths — "nothing to apply" and "tools
// applied" — because the daemon's destination has nothing to do with whether a
// tool's config file needed editing. Keeping it in one function is what stops
// those two paths from drifting apart again.
//
// It deliberately does NOT run for a dry run (which must not touch the machine)
// or after an aborted confirmation (where the person said change nothing).
func adoptOnboarding(ob *api.Onboarding, say func(string)) error {
	if err := config.SaveHookConfig(ob.Endpoint, ob.IngestToken); err != nil {
		return err
	}
	// Reporting the DESTINATION, not just the path: this line used to print
	// "✓ Hook ~/.keld/hook.json" unconditionally, above a return that wrote
	// nothing, so an install log showed the hook being configured on exactly
	// the run that left it stale.
	say(fmt.Sprintf("  ✓ %-26s %s → %s", "Hook", "~/.keld/hook.json", ob.Endpoint))
	m, err := config.LoadManifest()
	if err != nil || m == nil {
		// A missing or unreadable manifest is not a setup failure: hook.json is
		// the file the daemon reads, and it is already written. The apply path
		// below builds and saves a full manifest of its own.
		return nil
	}
	m.Hook = &config.HookRecord{Version: version.CLI}
	// ⚠️ **THE RECORDED ENDPOINT IS STAMPED HERE, NOT ONLY ON THE APPLY PATH.**
	// It used to be written solely where runSetup rebuilds the whole manifest —
	// i.e. only when a tool config actually changed — so on the ordinary upgrade
	// where every tool reported "already configured" the field kept whatever
	// Atlas it last saw. Measured on a real machine: the manifest named
	// localhost:3000 while the daemon published to localhost:8000 out of
	// hook.json.
	//
	// The field is KEPT rather than removed because manifest.json has always
	// carried it and an older reader (the Python CLI's own manifest format) would
	// break on its absence; nothing in this repo reads it back any more — see
	// pairedEndpoint. Written from the same verified onboarding hook.json is
	// written from, one line above, so the two cannot disagree.
	m.Endpoint = &ob.Endpoint
	return m.Save()
}

func runSetup(adapters []tools.Adapter, p tools.SetupParams, client *api.Client, ob *api.Onboarding, opts SetupOpts) (*config.Manifest, error) {
	// ⚠️ THE ORG INGEST TOKEN MUST NEVER REACH A TOOL CONFIG. This is the one
	// place the two credentials are both in scope, so it is the only place the
	// rule can be enforced rather than merely intended. A tool holding an Atlas
	// credential goes stale the moment that credential rotates and only
	// restarting the tool recovers it — measured, 40 minutes of dead telemetry
	// while `keld signal doctor` correctly reported no problems, because the
	// stale copy lives inside a process it cannot inspect.
	//
	// A refactor that reconnects p.IngestToken to ob.IngestToken would silently
	// restore that bug; this fails loudly instead.
	if ob != nil && ob.IngestToken != "" && p.IngestToken == ob.IngestToken {
		return nil, errors.New("refusing to write the Atlas ingest token into a tool config: " +
			"tools must point at the daemon's telemetry proxy and hold only the local secret " +
			"(see docs/superpowers/specs/2026-08-27-telemetry-loopback-proxy-design.md)")
	}
	// ⚠️ THE OLDER OF TWO INSTALLS MUST NOT WRITE THE MACHINE'S TOOL CONFIGS.
	// Measured 2026-09-18: a keld 3.0.0-rc.3 at /usr/local/keld/keld wrote
	// telemetry secret a5629e92… into ~/.codex/config.toml and
	// ~/.claude/settings.json while the running proxy held 26908e20…, because
	// that binary predates the secret having a file of its own. Codex's
	// telemetry was dead and setup had reported success.
	//
	// Checked BEFORE anything is read or written, so the refusal costs nothing
	// and leaves nothing half-done. A dry run is exempt: it changes nothing, and
	// the wizard's preview pane runs it before anyone has agreed to anything.
	if !opts.DryRun {
		if path, ver := newerKeldOnPATH(version.CLI); path != "" {
			console.Print("")
			console.Print(fmt.Sprintf("  ✗ A newer keld (%s) is on PATH at %s", ver, path))
			return nil, shadowedByNewerKeld(path, ver, version.CLI)
		}
	}
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
	// skipConflict prints the unified single-line outcome for a tool that is
	// skipped because of a config conflict, regardless of which of the three
	// paths (dry-run / --yes / interactive) led there.
	skipConflict := func(adapter tools.Adapter) {
		say(fmt.Sprintf("  ⚠ %-26s skipped (conflict)", adapter.DisplayName()))
	}

	say("")
	say("Configuring your AI tools…")

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

		plan := adapter.Apply(before, p, false)

		if plan.Conflict != "" {
			say(fmt.Sprintf("  ⚠ %-26s conflict: %s", adapter.DisplayName(), plan.Conflict))
			if opts.DryRun {
				skipConflict(adapter)
				emit(SetupEvent{Kind: "tool", Name: adapter.Name(), Display: adapter.DisplayName(), Action: "skipped_conflict", Path: path})
				continue
			}
			if opts.Yes {
				skipConflict(adapter)
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
					skipConflict(adapter)
					emit(SetupEvent{Kind: "tool", Name: adapter.Name(), Display: adapter.DisplayName(), Action: "skipped_conflict", Path: path})
					continue
				}
				if !quiet && opts.ShowDiff {
					diffview.Render(before, plan.AfterText, plan.ConfigPath)
				}
				approveds = append(approveds, approved{adapter, plan})
				continue
			}
			skipConflict(adapter)
			emit(SetupEvent{Kind: "tool", Name: adapter.Name(), Display: adapter.DisplayName(), Action: "skipped_conflict", Path: path})
			continue
		}

		if !plan.Changed {
			say(fmt.Sprintf("  ✓ %-26s already configured", adapter.DisplayName()))
			emit(SetupEvent{Kind: "tool", Name: adapter.Name(), Display: adapter.DisplayName(), Action: "already_configured", Path: path})
			continue
		}

		if !quiet && opts.ShowDiff {
			diffview.Render(before, plan.AfterText, plan.ConfigPath)
		}
		approveds = append(approveds, approved{adapter, plan})
	}

	if opts.DryRun {
		// ⚠️ A dry run must say what it WOULD do, not just what it is skipping.
		// Every event above this point is skipped_conflict/already_configured;
		// an adapter that is detected, unconflicted and changed reached
		// `approveds` with no event of its own, because the "configured" event
		// is emitted later, inside the write loop a dry run never reaches. The
		// macOS wizard pane renders exactly the `tool` events runSetup emits,
		// so on a fresh Mac with (say) Claude Code installed and unconfigured —
		// the common case — the pane saw nothing and rendered "No supported AI
		// tools found on this Mac." while the tool sat right there.
		for _, a := range approveds {
			emit(SetupEvent{Kind: "tool", Name: a.adapter.Name(), Display: a.adapter.DisplayName(),
				Action: "will_configure", Path: a.plan.ConfigPath})
		}
		return config.LoadManifest()
	}
	if len(approveds) == 0 {
		// ⚠️ NOTHING TO APPLY IS NOT NOTHING TO WRITE. This is the ordinary
		// state of every re-install and every upgrade — the tools are already
		// configured — and `SaveHookConfig` used to sit BELOW this return, so a
		// verified onboarding was thrown away and whatever hook.json the machine
		// already had survived.
		//
		// Measured on a real v3.0.0 install (2026-09-16): the wizard signed in
		// to production, postinstall ran setup, every tool reported "already
		// configured", and the daemon went on reporting to a dev Atlas from the
		// day before — 882 calls to localhost against 9 to atlas.keld.co — while
		// `status` displayed the production login and `doctor` found no problem,
		// because nothing compared the two. An installer that leaves the machine
		// internally inconsistent is worse than one that fails loudly.
		if err := adoptOnboarding(ob, say); err != nil {
			return nil, err
		}
		// The per-tool "already configured" / "skipped (conflict)" lines above
		// already convey the outcome; no separate "Nothing to apply." summary.
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
	if err := adoptOnboarding(ob, say); err != nil {
		return nil, err
	}

	for _, a := range approveds {
		// tools.CommitPlan, not three calls here: the daemon's integrations
		// detector applies the same adapters to the same files and must take
		// the same three steps in the same order — backup, atomic write, extra
		// file. A second write path is a second chance to forget the backup.
		backup, err := tools.CommitPlan(a.adapter, a.plan)
		if err != nil {
			return nil, err
		}
		var backupPtr *string
		if backup != "" {
			backupPtr = &backup
		}
		// ⚠️ `configured_at` is stamped by BOTH paths that apply an adapter —
		// here and in integrations.ApplyEntry — because it is the only record
		// of when keld wrote a tool's config, and the tools rewrite those files
		// themselves (measured 2026-09-18: Codex at session start, Claude Code
		// unprompted). A machine set up through this command and a machine set
		// up by the detector must answer the restart question the same way.
		wroteAt := time.Now().UTC()
		manifest.Tools[a.adapter.Name()] = config.ToolManifest{
			Name:         a.adapter.Name(),
			ConfigPath:   a.plan.ConfigPath,
			Managed:      a.plan.Managed,
			BackupPath:   backupPtr,
			ConfiguredAt: &wroteAt,
		}
		line := fmt.Sprintf("  ✓ %-26s configured", a.adapter.DisplayName())
		if backup != "" {
			line += fmt.Sprintf(" (backed up %s)", backup)
		}
		say(line)
		emit(SetupEvent{Kind: "tool", Name: a.adapter.Name(), Display: a.adapter.DisplayName(), Action: "configured", Path: a.plan.ConfigPath, Backup: backup})
	}

	if err := manifest.Save(); err != nil {
		return nil, err
	}

	// ⚠️ VERIFY WHAT WAS JUST WRITTEN, AGAINST THE PROXY THAT HAS TO ACCEPT IT.
	// Every check above this point asks whether the tool configs were written
	// correctly; none can tell whether the credential in them WORKS. On
	// 2026-09-18 that gap was the whole failure: the files were written exactly
	// as intended, with a secret the running daemon rejected, and setup said
	// nothing. One loopback POST closes it.
	//
	// No daemon listening is NOT a failure — `keld-agent install` starts the
	// service after this runs, so the ordinary first install has nothing to ask.
	switch outcome, code := probeTelemetry(p.Endpoint, p.IngestToken); outcome {
	case probeRejected:
		// Leaving the rejected credential in place would leave the machine in
		// the state the probe just proved broken, so the rollback is part of the
		// refusal rather than a courtesy. The secret itself is never printed —
		// a terminal, a CI log and an installer transcript are all places it
		// would then live.
		say("")
		say(fmt.Sprintf("  ✗ The running daemon REJECTED (%d) the telemetry credential just written.", code))
		say("    Your tools would have been configured with a secret it does not accept.")
		say("    Rolling back the tool configs.")
		if err := runRestore(manifest, nil, true, false, stdinConfirm); err != nil {
			return nil, fmt.Errorf("telemetry credential rejected (%d) and the rollback failed: %w", code, err)
		}
		return nil, fmt.Errorf("the running daemon rejected the telemetry credential (HTTP %d); "+
			"tool configs were rolled back. Restart the daemon (`keld-agent restart`) and re-run setup", code)
	case probeUnverified:
		say("")
		say("  • could not verify (daemon not running) — the tools are configured;")
		say("    the check runs against the loopback proxy, which starts with the agent.")
	}

	// A tool that is ALREADY RUNNING read its configuration at startup and will
	// go on posting to the previous destination until it is restarted. Say so:
	// this is the one part of setup a human has to do, and it is invisible —
	// telemetry simply never arrives, while enrichment (read from the transcript
	// by the daemon) keeps working and makes the machine look healthy.
	say("")
	say("  ⚠ Restart any running AI tools to finish.")
	say("    They read this configuration once at startup, so a tool that is")
	say("    already open keeps sending to the previous destination.")
	emit(SetupEvent{Kind: "done", Configured: len(manifest.Tools), Endpoint: ob.Endpoint,
		RestartRequired: true})
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
	var binPath string

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

			tp, err := telemetryTarget()
			if err != nil {
				return err
			}
			p := tools.SetupParams{
				Endpoint:    tp.Endpoint,
				IngestToken: tp.Secret,
				BinPath:     resolveSetupBinPath(binPath),
				// ⚠️ The tool's OWN OTLP export is opt-in and OFF by default.
				// Signal reads the same usage off the tool's transcript, and
				// this is the one lane that requires a credential to live inside
				// a file the tool reads once at startup — so setup writes no
				// OTEL block unless this machine asked for one, and takes out a
				// block an earlier keld left. Same resolution the daemon's
				// detector uses, so a machine set up by hand and one set up by
				// the poll agree. See settings.Settings.ToolOTLP.
				ToolOTLP: settings.Load().ToolOTLPEnabled(),
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
						emitEvent(doneEvent{Event: "done", Configured: e.Configured, Endpoint: e.Endpoint,
							RestartRequired: e.RestartRequired})
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
	cmd.Flags().StringVar(&binPath, "bin-path", "",
		"Absolute path of the keld binary to pin into tool hooks (default: the running binary).")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable NDJSON events on stdout (implies --yes).")

	return cmd
}
