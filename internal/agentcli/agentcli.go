// Package agentcli is the cobra root for the keld-agent binary.
package agentcli

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/ncx-ai/keld-signal/internal/agent/daemon"
	"github.com/ncx-ai/keld-signal/internal/agent/service"
	"github.com/ncx-ai/keld-signal/internal/agent/settings"
	"github.com/ncx-ai/keld-signal/internal/auth"
	"github.com/ncx-ai/keld-signal/internal/console"
	"github.com/ncx-ai/keld-signal/internal/paths"
	"github.com/ncx-ai/keld-signal/internal/version"
)

// reauthRequiredLine is the human line surfaced by `keld-agent status` (and,
// with identical wording, `keld signal status`/`doctor` — internal/cli) when
// the daemon's local re-authentication marker (paths.ReauthMarkerPath) is
// present: the CLI token itself is gone/revoked and the daemon self-heal
// can't recover it without a human `keld login` + restart.
const reauthRequiredLine = "⚠ re-authentication required — run 'keld login', then 'keld-agent restart'"

// printStatus implements `keld-agent status`: print the service status, then
// (best-effort, read-only) surface the daemon's local re-authentication
// marker if present. statusFn/reauthFn are seams for testing — production
// wires service.Status and paths.ReauthRequired.
func printStatus(statusFn func() (string, error), reauthFn func() (bool, string)) error {
	s, err := statusFn()
	if err != nil {
		return err
	}
	fmt.Println(s)
	if required, _ := reauthFn(); required {
		fmt.Println(reauthRequiredLine)
	}
	return nil
}

// keldName is the platform basename of the keld CLI binary.
func keldName() string {
	if runtime.GOOS == "windows" {
		return "keld.exe"
	}
	return "keld"
}

// isRegularFile reports whether p exists and is a regular file.
func isRegularFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.Mode().IsRegular()
}

// keldInDir returns the path to a regular-file keld binary in dir, if present.
func keldInDir(dir string) (string, bool) {
	p := filepath.Join(dir, keldName())
	if isRegularFile(p) {
		return p, true
	}
	return "", false
}

// resolveKeld locates the keld CLI binary: first beside the running keld-agent
// executable (how the installers lay it out), then on PATH.
func resolveKeld() (string, error) {
	if exe, err := os.Executable(); err == nil {
		if p, ok := keldInDir(filepath.Dir(exe)); ok {
			return p, nil
		}
	}
	if p, err := exec.LookPath(keldName()); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("keld binary not found beside keld-agent or on PATH; install keld first")
}

// stepRunner runs a keld subcommand. The production implementation execs it
// with the parent's stdio so interactive flows (device auth, config diffs) work.
type stepRunner func(name string, args ...string) error

// runStep is the production stepRunner: run the command with inherited stdio.
func runStep(name string, args ...string) error {
	c := exec.Command(name, args...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// installConfig carries the install command's onboarding knobs.
type installConfig struct {
	code    string // one-time setup code; when set, onboarding runs headless
	apiURL  string // --api-url passthrough for local dev
	yes     bool   // pass --yes to signal setup (implied when code is set)
	jsonOut bool   // --json passthrough for installer UIs
	// headless forces the non-interactive branch regardless of what isTTY says.
	// It exists because TTY DETECTION IS NOT A RELIABLE PROXY FOR "a human can
	// answer this" ON WINDOWS: Inno Setup's `runhidden` launches a console app
	// with STARTF_USESHOWWINDOW/SW_HIDE and does not redirect stdio, so the child
	// still owns a real console — just one nobody can see. stdout is a console
	// handle, term.IsTerminal answers true, and install took the INTERACTIVE
	// branch inside an invisible window. Observed on a real machine: the .iss's
	// registration entry spawned `keld.exe signal setup`, which blocked forever
	// on stdinConfirm's Fscanln (internal/cli/setup.go), Inno waited on it, and
	// the installer sat at "Registering the Keld agent..." until that process was
	// killed by hand — after which onboarding asked for a login a SECOND time,
	// because the first one had already happened where nobody could see it.
	// A caller that knows there is no reachable human says so; nothing is inferred.
	headless bool
	// backend is the ml_backend a v2 install lands on. Default "deterministic"
	// (resolved in runInstall, so a zero-value installConfig in a test lands
	// where a real install does); "auto" restores the pre-v2 ML pipeline and
	// "off" disables enrichment. It is written to agent-config.json because
	// ml_backend has NO REMOTE OVERRIDE — the installer is the only lever that
	// will ever exist for it.
	backend string
}

// runInstall writes the v2 settings, sets the user up, then registers the service.
//
// ORDER MATTERS TWICE. The daemon refuses to run until signal setup has written
// ~/.keld/hook.json and installService starts it immediately, so service install
// runs LAST. And ml_backend is read at daemon STARTUP and never re-read, so the
// config write runs FIRST — the restart inside installService (launchctl
// bootout+bootstrap / systemctl restart / schtasks /End+/Run) is what makes the
// new mode take effect in this same run. On macOS that is not academic: the pkg's
// postinstall kickstarts the agent BEFORE opening onboard.command, so a daemon is
// already running on the old settings when this is called.
//
// The config write happens in EVERY branch, the headless one included: a machine
// that only registers the service still has to be configured, or a GUI-installer
// install runs v1 behaviour until someone notices.
//
// With a setup code the login+setup run non-interactively regardless of TTY;
// without a code they run only in a real terminal — or not at all when the caller
// passed --headless, which overrides the TTY probe outright.
func runInstall(cfg installConfig, isTTY func() bool, resolveKeld func() (string, error),
	run stepRunner, writeConfig func(backend string, blocks bool) error, installService func() error) error {
	backend := cfg.backend
	if backend == "" {
		backend = "deterministic"
	}
	// blocks is always true: it is what v2 IS. An operator who wants it off sets
	// KELD_BLOCKS=0, which wins over the config file in both directions.
	if err := writeConfig(backend, true); err != nil {
		return fmt.Errorf("write agent-config.json: %w", err)
	}

	// A setup code may carry the host that minted it. Resolve it HERE rather than
	// leaving it to `keld login`: cfg.apiURL is what puts --api-url on BOTH child
	// commands, and `signal setup` has to target the same host as the login or it
	// writes the previous endpoint into hook.json — a split-brain install where
	// auth and telemetry point at different deploys and nothing reports an error.
	if cfg.code != "" {
		host, bare, err := auth.ParsePairingCode(cfg.code)
		if err != nil {
			return err
		}
		cfg.code = bare
		if host != "" && cfg.apiURL == "" {
			cfg.apiURL = host
		}
	}

	login := []string{"login"}
	setup := []string{"signal", "setup"}
	if cfg.apiURL != "" {
		login = append(login, "--api-url", cfg.apiURL)
		setup = append(setup, "--api-url", cfg.apiURL)
	}
	if cfg.jsonOut {
		login = append(login, "--json")
		setup = append(setup, "--json")
	}

	switch {
	case cfg.code != "":
		keld, err := resolveKeld()
		if err != nil {
			return err
		}
		login = append(login, "--code", cfg.code)
		setup = append(setup, "--yes")
		if err := run(keld, login...); err != nil {
			return fmt.Errorf("keld login: %w", err)
		}
		if err := run(keld, setup...); err != nil {
			return fmt.Errorf("keld signal setup: %w", err)
		}
	case !cfg.headless && isTTY():
		keld, err := resolveKeld()
		if err != nil {
			return err
		}
		if cfg.yes {
			setup = append(setup, "--yes")
		}
		if err := run(keld, login...); err != nil {
			return fmt.Errorf("keld login: %w", err)
		}
		if err := run(keld, setup...); err != nil {
			return fmt.Errorf("keld signal setup: %w", err)
		}
	default:
		fmt.Println("Service installed. Finish setup by running: keld login && keld signal setup")
	}

	if !cfg.jsonOut {
		console.Print("")
		console.Print("Starting the agent…")
	}
	if err := installService(); err != nil {
		return err
	}
	if !cfg.jsonOut {
		console.Print("  ✓ keld-agent running — enrichment stays on-device; only masked signal is sent")
	}
	return nil
}

// stdoutIsTTY reports whether stdout is an interactive terminal. Detection keys on
// stdout, NOT stdin: under `curl | sh` the installer — and the keld-agent it spawns —
// inherit the pipe as stdin, so a stdin check misreads a human in a real terminal as
// headless. Interactive device-flow login needs no stdin (it prints a URL and polls),
// so a piped stdin never blocks it. A GUI installer that redirects or detaches stdio
// (launchd) has no terminal on stdout either, so the headless branch is selected there.
//
// ⚠️ IT IS NOT SELECTED UNDER INNO SETUP'S `runhidden`, and this comment used to name
// runhidden as a case it handled. `runhidden` hides the WINDOW; it does not take the
// console away, so stdout is still a console handle and this returns true inside a
// window no human can reach. That is why installConfig.headless exists: on Windows the
// installer states its intent explicitly rather than relying on this probe.
func stdoutIsTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}

// NewRootCmd builds the keld-agent command tree.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:           "keld-agent",
		Short:         "Keld enrichment daemon",
		Version:       version.CLI,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	runCmd := &cobra.Command{
		Use:   "run",
		Short: "Run the enrichment daemon in the foreground.",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Windows only, and only when the CALLER says so. The scheduled task
			// passes --hide-console so a logon does not put a black window full of
			// daemon logs on the user's screen; a human running `keld-agent run`
			// passes nothing and keeps their terminal. The previous version
			// INFERRED this from the console's process count and got it wrong on a
			// real machine — see console_windows.go.
			if hide, _ := cmd.Flags().GetBool("hide-console"); hide {
				hideOwnConsole()
			}
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return daemon.Run(ctx)
		},
	}
	runCmd.Flags().Bool("hide-console", false,
		"Detach from the console at startup (Windows). Set by the scheduled task so a logon shows no window; omit it to watch the daemon's logs in your own terminal.")
	root.AddCommand(runCmd)
	installCmd := &cobra.Command{
		Use:   "install",
		Short: "Log in, set up telemetry, and install keld-agent as a per-user autostart service.",
		RunE: func(cmd *cobra.Command, args []string) error {
			code, _ := cmd.Flags().GetString("code")
			if code == "" {
				code = os.Getenv("KELD_SETUP_CODE") // flag wins; fall back to the env var
			}
			yes, _ := cmd.Flags().GetBool("yes")
			apiURL, _ := cmd.Flags().GetString("api-url")
			jsonOut, _ := cmd.Flags().GetBool("json")
			backend, _ := cmd.Flags().GetString("backend")
			headless, _ := cmd.Flags().GetBool("headless")
			cfg := installConfig{code: code, apiURL: apiURL, yes: yes, jsonOut: jsonOut,
				backend: backend, headless: headless}
			return runInstall(cfg, stdoutIsTTY, resolveKeld, runStep,
				settings.WriteInstallDefaults, service.Install)
		},
	}
	installCmd.Flags().String("code", "", "Redeem a one-time setup code for a non-interactive login (defaults to $KELD_SETUP_CODE).")
	installCmd.Flags().Bool("yes", false, "Skip confirmation prompts during setup.")
	installCmd.Flags().String("api-url", "", "Target a different Keld API base URL (e.g. http://localhost:8000) for local dev.")
	installCmd.Flags().Bool("json", false, "Emit machine-readable NDJSON from login/setup (for installer UIs).")
	installCmd.Flags().Bool("headless", false,
		"Never prompt: register the service and skip login/setup even if stdout looks like a terminal. For GUI installers that run this with no console a human can reach.")
	installCmd.Flags().String("backend", "deterministic",
		"Enrichment backend to configure: deterministic (v2 default — no model download), auto (the GLiNER2 pipeline), or off.")
	root.AddCommand(installCmd)
	root.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Remove the keld-agent service.",
		RunE:  func(cmd *cobra.Command, args []string) error { return service.Uninstall() },
	})
	root.AddCommand(newMetricsCmd())
	root.AddCommand(newEnrichCmd())
	root.AddCommand(newEvalCmd())
	root.AddCommand(newStudyCmd())
	root.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show keld-agent service status.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return printStatus(service.Status, paths.ReauthRequired)
		},
	})
	for _, c := range serviceControlCmds() {
		root.AddCommand(c)
	}
	return root
}

// serviceControlCmds builds the start/stop/restart lifecycle commands for the
// installed keld-agent service. Shared verb→action table so keld-agent and
// `keld signal` expose the same three controls.
func serviceControlCmds() []*cobra.Command {
	type ctl struct {
		use, short, done string
		run              func() error
	}
	ctls := []ctl{
		{"start", "Start the keld-agent background service.", "started", service.Start},
		{"stop", "Stop the keld-agent background service.", "stopped", service.Stop},
		{"restart", "Restart the keld-agent background service (picks up a new binary).", "restarted", service.Restart},
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
				fmt.Printf("keld-agent %s.\n", c.done)
				return nil
			},
		})
	}
	return cmds
}

// executeCmd runs root and, on error, prints it to stderr (once) before
// returning exit code 1. The root command keeps SilenceErrors/SilenceUsage so
// cobra prints neither the error nor usage; printing here is the single place a
// returned error becomes visible. Without this, a daemon.Run failure exits 1
// with completely empty output. (An unconfigured agent is no longer such a
// failure: daemon.Run idles and waits for hook.json — see daemon.awaitConfig.)
func executeCmd(root *cobra.Command, stderr io.Writer) int {
	if err := root.Execute(); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// Execute runs the keld-agent CLI and returns an exit code.
func Execute() int { return executeCmd(NewRootCmd(), os.Stderr) }
