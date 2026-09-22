// Command keld-conform holds the moving parts of the conformance harness: the
// mock model a real tool is pointed at, the mock Atlas the daemon publishes to,
// and the checkpoint reader that says whether a chain step passed.
//
// It is a test tool. It is built by `make conformance` and is never shipped in
// an installer.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "mockllm":
		err = runMockLLM(args)
	case "mockatlas":
		err = runMockAtlas(args)
	case "check":
		err = runCheck(args)
	case "codex-approve":
		err = runCodexApprove(args)
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "keld-conform: unknown subcommand %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "keld-conform %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: keld-conform <subcommand> [flags]

subcommands:
  mockllm    serve the mock model (Anthropic Messages + OpenAI Responses)
  mockatlas  serve the mock Atlas (login, onboarding, publish, settings)
  check      read the five checkpoints and print a JSON verdict

  codex-approve  print the [hooks.state] block that approves keld's Codex hooks
                 for a HARNESS. Never run against a real config: it forges the
                 approval a human gives in Codex's /hooks screen.
`)
}
