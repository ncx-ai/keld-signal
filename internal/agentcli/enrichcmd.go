package agentcli

import (
	"fmt"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/localagent"
	"github.com/spf13/cobra"
)

// newEnrichCmd builds `keld-agent enrich`: run the enrichment pipeline on a
// test prompt and print the profile JSON. Local only — never published.
func newEnrichCmd() *cobra.Command {
	var forceDeterministic bool
	var source string
	cmd := &cobra.Command{
		Use:   "enrich [prompt]",
		Short: "Run enrichment on a test prompt and print the profile JSON (local; not published).",
		Long: "Run the enrichment pipeline on a test prompt and print the resulting " +
			"profile as JSON, for quick sanity checking and debugging.\n\n" +
			"The prompt is taken from the arguments, or from stdin if none are given. " +
			"Uses the running GLiNER2 sidecar when available, otherwise the deterministic " +
			"backend. The prompt is processed locally and never published to Atlas.\n\n" +
			"Tip: single-quote the prompt (or pipe it via stdin) so your shell does not " +
			"interpret backticks or $(...) as command substitution and splice command " +
			"output into the text being enriched.",
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			text, err := localagent.ReadPrompt(args, cmd.InOrStdin())
			if err != nil {
				return err
			}
			info, _ := agentcfg.Read()
			model, note, err := localagent.ResolveModel(info, forceDeterministic)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "keld-agent enrich: "+note)
			out, err := localagent.EnrichJSON(text, source, model)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), out)
			return nil
		},
	}
	cmd.Flags().BoolVar(&forceDeterministic, "deterministic", false,
		"Force the deterministic backend instead of the sidecar.")
	cmd.Flags().StringVar(&source, "source", "claude_code",
		"Tool source to attribute the prompt to (e.g. claude_code, codex).")
	return cmd
}
