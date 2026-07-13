// Package agentcli is the cobra root for the keld-agent binary.
package agentcli

import (
	"context"
	"fmt"
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
	"github.com/ncx-ai/keld-signal/internal/version"
)

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
}

// runInstall sets the user up, then registers the service. Order matters: the daemon
// refuses to run until signal setup has written ~/.keld/hook.json, and installService
// starts it immediately — so service install runs last. With a setup code the login+setup
// run non-interactively regardless of TTY; without a code they run only in a real terminal.
func runInstall(cfg installConfig, isTTY func() bool, resolveKeld func() (string, error), run stepRunner, installService func() error) error {
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
	case isTTY():
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
	return installService()
}

// stdoutIsTTY reports whether stdout is an interactive terminal. Detection keys on
// stdout, NOT stdin: under `curl | sh` the installer — and the keld-agent it spawns —
// inherit the pipe as stdin, so a stdin check misreads a human in a real terminal as
// headless. Interactive device-flow login needs no stdin (it prints a URL and polls),
// so a piped stdin never blocks it. A GUI installer (launchd/runhidden) has no terminal
// on stdout either, so the headless branch is still selected there.
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
	root.AddCommand(&cobra.Command{
		Use:   "run",
		Short: "Run the enrichment daemon in the foreground.",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return daemon.Run(ctx)
		},
	})
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
			cfg := installConfig{code: code, apiURL: apiURL, yes: yes, jsonOut: jsonOut}
			return runInstall(cfg, stdoutIsTTY, resolveKeld, runStep, service.Install)
		},
	}
	installCmd.Flags().String("code", "", "Redeem a one-time setup code for a non-interactive login (defaults to $KELD_SETUP_CODE).")
	installCmd.Flags().Bool("yes", false, "Skip confirmation prompts during setup.")
	installCmd.Flags().String("api-url", "", "Target a different Keld API base URL (e.g. http://localhost:8000) for local dev.")
	installCmd.Flags().Bool("json", false, "Emit machine-readable NDJSON from login/setup (for installer UIs).")
	root.AddCommand(installCmd)
	root.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Remove the keld-agent service.",
		RunE:  func(cmd *cobra.Command, args []string) error { return service.Uninstall() },
	})
	root.AddCommand(newMetricsCmd())
	root.AddCommand(newEnrichCmd())
	root.AddCommand(&cobra.Command{
		Use:   "status",
		Short: "Show keld-agent service status.",
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := service.Status()
			if err != nil {
				return err
			}
			fmt.Println(s)
			return nil
		},
	})
	return root
}

// Execute runs the keld-agent CLI and returns an exit code.
func Execute() int {
	if err := NewRootCmd().Execute(); err != nil {
		return 1
	}
	return 0
}
