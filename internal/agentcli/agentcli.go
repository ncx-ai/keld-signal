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

// runInstall sets the user up, then registers the service. Order matters: the
// daemon refuses to run until signal setup has written ~/.keld/hook.json, and
// installService starts it immediately — so service install runs last.
func runInstall(isTTY func() bool, resolveKeld func() (string, error), run stepRunner, installService func() error) error {
	if isTTY() {
		keld, err := resolveKeld()
		if err != nil {
			return err
		}
		if err := run(keld, "login"); err != nil {
			return fmt.Errorf("keld login: %w", err)
		}
		if err := run(keld, "signal", "setup"); err != nil {
			return fmt.Errorf("keld signal setup: %w", err)
		}
	} else {
		fmt.Println("Service installed. Finish setup by running: keld login && keld signal setup")
	}
	return installService()
}

// stdinIsTTY reports whether stdin is an interactive terminal. A GUI installer
// invokes `keld-agent install` with no console (Windows runhidden / macOS launchd
// session, which wires stdin to /dev/null) — a real isatty check is required
// because /dev/null is a character device and would fool a ModeCharDevice test.
// When stdin is not a terminal the interactive login/setup steps are skipped and
// the installer's own pages drive `keld --json` instead.
func stdinIsTTY() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
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
	root.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Log in, set up telemetry, and install keld-agent as a per-user autostart service.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInstall(stdinIsTTY, resolveKeld, runStep, service.Install)
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "uninstall",
		Short: "Remove the keld-agent service.",
		RunE:  func(cmd *cobra.Command, args []string) error { return service.Uninstall() },
	})
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
