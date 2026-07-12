package agentcli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/spf13/cobra"
)

// readPrompt returns the prompt text from args (joined) or, if none, from stdin.
func readPrompt(args []string, stdin io.Reader) (string, error) {
	if len(args) > 0 {
		return strings.Join(args, " "), nil
	}
	b, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
	if err != nil {
		return "", err
	}
	text := strings.TrimSpace(string(b))
	if text == "" {
		return "", errors.New("no prompt: pass text as an argument or on stdin")
	}
	return text, nil
}

// resolveEnrichModel picks the enrichment backend and returns a human note
// naming it. It uses the running sidecar (discovered via agent.json) when one is
// available, else the deterministic backend. forceDeterministic always picks
// deterministic — useful to compare backends or when the sidecar is unwanted.
func resolveEnrichModel(info *agentcfg.Info, forceDeterministic bool) (enrich.Model, string) {
	if !forceDeterministic && info != nil && info.SidecarPort != 0 {
		url := fmt.Sprintf("http://127.0.0.1:%d", info.SidecarPort)
		// Generous per-call timeout: the pipeline issues up to 7 sidecar calls
		// and CPU inference can be slow on a busy host.
		return sidecar.New(url, 30*time.Second), "using live GLiNER2 sidecar at " + url
	}
	if forceDeterministic {
		return enrich.NewDeterministic(), "using deterministic backend (--deterministic)"
	}
	return enrich.NewDeterministic(), "sidecar not running; using deterministic backend"
}

// newEnrichCmd builds `keld-agent enrich`: run the enrichment pipeline on a test
// prompt and print the resulting profile as JSON. Local only — it reads the
// prompt on this machine, classifies it, and prints; it never publishes to
// Atlas. Handy for eyeballing classification quality and detected entities.
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
			text, err := readPrompt(args, cmd.InOrStdin())
			if err != nil {
				return err
			}
			info, _ := agentcfg.Read()
			model, note := resolveEnrichModel(info, forceDeterministic)
			fmt.Fprintln(cmd.ErrOrStderr(), "keld-agent enrich: "+note)

			cwd, _ := os.Getwd()
			meta := enrich.Meta{Repo: cwd, Tool: source}
			profile := enrich.Run(text, source, meta, model)

			out, err := json.MarshalIndent(profile, "", "  ")
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}
	cmd.Flags().BoolVar(&forceDeterministic, "deterministic", false,
		"Force the deterministic backend instead of the sidecar.")
	cmd.Flags().StringVar(&source, "source", "claude_code",
		"Tool source to attribute the prompt to (e.g. claude_code, codex).")
	return cmd
}
