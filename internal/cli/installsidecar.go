package cli

import (
	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/errs"
	"github.com/ncx-ai/keld-signal/internal/sidecarinstall"
)

// reportSidecarInstall says where the sidecar went AND whether the running one
// is now that sidecar. Those are different facts: the swap can succeed while the
// restart does not, which leaves a correct tree on disk and a stale process
// serving — the exact state doctor's version-skew check reports.
func reportSidecarInstall(res sidecarinstall.Result, jsonOut bool) {
	if jsonOut {
		emitEvent(sidecarInstalledEvent{
			Event: "installed", Path: res.Path, Version: res.Version,
			Restarted: res.Restarted, RestartError: res.RestartErr,
		})
		return
	}
	console.Print("  ✓ analysis sidecar → " + res.Path)
	if !res.Restarted && res.RestartErr != "" {
		console.Print("    ⚠ could not restart the agent (" + res.RestartErr + ") — " +
			"the previous sidecar keeps running until it restarts. `keld-agent restart` finishes it.")
	}
}

type sidecarProgressEvent struct {
	Event    string `json:"event"`
	Received int64  `json:"received"`
	Total    int64  `json:"total"`
}

type sidecarStagedEvent struct {
	Event   string `json:"event"`
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
}

type sidecarInstalledEvent struct {
	Event   string `json:"event"`
	Path    string `json:"path"`
	Version string `json:"version,omitempty"`
	// Whether the RUNNING sidecar is now the one just installed. Omitted when
	// the restart succeeded and nothing needs saying; a consumer that sees
	// restart_error knows the tree is current and the process is not.
	Restarted    bool   `json:"restarted,omitempty"`
	RestartError string `json:"restart_error,omitempty"`
}

func newInstallSidecarCmd() *cobra.Command {
	var (
		jsonOut    bool
		tag        string
		dest       string
		stageOnly  bool
		commit     string
		baseURL    string
		cleanupJob string
	)
	cmd := &cobra.Command{
		Use:   "install-sidecar",
		Short: "Download and install the analysis sidecar.",
		Long: "Download, verify and install the Keld analysis sidecar (~190MB).\n" +
			"The macOS installer's setup pane drives this with --json and renders the events.",
		RunE: func(cmd *cobra.Command, args []string) error {
			fail := func(err error) error {
				if jsonOut {
					emitEvent(errorEvent{Event: "error", Message: cleanErrorMessage(err)})
					return errs.ErrSilentExit
				}
				return err
			}
			if commit != "" {
				d := dest
				if d == "" {
					var err error
					if d, err = sidecarinstall.DestDir(); err != nil {
						return fail(err)
					}
				}
				res, err := sidecarinstall.Commit(commit, d)
				if err != nil {
					return fail(err)
				}
				reportSidecarInstall(res, jsonOut)
				return nil
			}

			opts := sidecarinstall.Opts{BaseURL: baseURL, Tag: tag, Dest: dest, StageOnly: stageOnly, CleanupJob: cleanupJob}
			if jsonOut {
				opts.Progress = sidecarinstall.NewProgressThrottle(func(received, total int64) {
					emitEvent(sidecarProgressEvent{Event: "progress", Received: received, Total: total})
				})
				opts.Warn = func(msg string) {
					emitEvent(warningEvent{Event: "warning", Message: msg})
				}
			}
			res, err := sidecarinstall.Install(opts)
			if err != nil {
				return fail(err)
			}
			switch {
			case stageOnly && jsonOut:
				emitEvent(sidecarStagedEvent{Event: "staged", Path: res.StagedPath, Version: res.Version})
			case stageOnly:
				console.Print("  ✓ analysis sidecar staged at " + res.StagedPath)
			default:
				reportSidecarInstall(res, jsonOut)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable NDJSON events on stdout.")
	cmd.Flags().StringVar(&tag, "tag", "", "Release tag to fetch (default: the latest release).")
	cmd.Flags().StringVar(&dest, "dest", "", "Directory that holds keld-agent-sidecar/ (default: ~/.local/bin).")
	cmd.Flags().BoolVar(&stageOnly, "stage-only", false, "Download and unpack, but do not replace the installed sidecar.")
	cmd.Flags().StringVar(&commit, "commit", "", "Install a previously staged tree (the path from --stage-only).")
	cmd.Flags().StringVar(&baseURL, "base-url", "", "Release download base URL (testing).")
	cmd.Flags().StringVar(&cleanupJob, "cleanup-job", "",
		"launchd plist to delete after a successful install (the installer's one-shot fetch job).")
	return cmd
}
