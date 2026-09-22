package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ncx-ai/keld-signal/internal/tools"
)

// runCodexApprove prints the `[hooks.state]` block that puts a harness's Codex
// into the approved state, for the hooks the given config declares now.
//
// ⚠️ A HARNESS TOOL, NOT A SETUP STEP. Writing this into a real config.toml
// forges a human's approval in `/hooks`; keld never does that, and nothing
// shipped in an installer can reach this binary. What it is for is the
// conformance chain and the Playwright journey, which must reach the approved
// state without a person driving a TUI.
//
// It exists because approval stopped being a constant the day the trusted hash
// was actually compared: a hand-written block with a placeholder hash asserted
// a machine state Codex can never be in, and the e2e journey failed on it.
func runCodexApprove(args []string) error {
	fs := flag.NewFlagSet("codex-approve", flag.ContinueOnError)
	config := fs.String("config", "", "path to the config.toml whose hooks are being approved")
	substr := fs.String("command-substr", "keld", "substring identifying keld's own hook command")
	source := fs.String("source", "", "the path Codex names in its state key (default: --config's path)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *config == "" {
		return fmt.Errorf("--config is required")
	}
	if *source == "" {
		*source = *config
	}
	b, err := os.ReadFile(*config)
	if err != nil {
		return err
	}
	block, err := tools.CodexApprovalTOML(b, *substr, *source)
	if err != nil {
		return err
	}
	fmt.Print(block)
	return nil
}
