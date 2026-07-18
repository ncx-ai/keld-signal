package agentcli

import (
	"fmt"

	"github.com/ncx-ai/keld-signal/internal/agent/agentcfg"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/eval"
	"github.com/ncx-ai/keld-signal/internal/localagent"
	"github.com/spf13/cobra"
)

// newEvalCmd builds `keld-agent eval`: run the enrichment pipeline over the
// embedded gold set (and, with --confound, the confound set) against the live
// GLiNER2 sidecar and print a per-facet metric table. Local only; never
// publishes. This is the measurement substrate for classification experiments.
func newEvalCmd() *cobra.Command {
	var withContext, withConfound bool
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Score the enrichment pipeline against the gold/confound sets (local; uses the live sidecar).",
		RunE: func(cmd *cobra.Command, args []string) error {
			info, _ := agentcfg.Read()
			model, note, err := localagent.ResolveModel(info)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "keld-agent eval: "+note)

			rows, err := eval.LoadGold()
			if err != nil {
				return err
			}
			if withConfound {
				cf, err := eval.LoadConfound()
				if err != nil {
					return err
				}
				rows = append(rows, cf...)
			}

			var pred []eval.Pred
			if withContext {
				pred = eval.RunModelWithContext(model, rows)
			} else {
				pred = eval.RunModel(model, rows)
			}

			fields := []string{"task_type", "domain", "sensitivity", "activity_type", "function_guess", "speech_act", "subcategory"}
			m := eval.Score(rows, pred, fields)
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "context=%v confound=%v rows=%d\n", withContext, withConfound, len(rows))
			for _, f := range fields {
				fmt.Fprintf(out, "  %-15s accuracy=%.3f\n", f, m[f]["accuracy"])
			}
			fmt.Fprintf(out, "  %-15s sensitive_recall=%.3f\n", "sensitivity", m["sensitivity"]["sensitive_recall"])
			if withConfound {
				lk := eval.LeakageRate(rows, pred)
				fmt.Fprintf(out, "  leakage(function_guess)=%.3f  leakage(task_type)=%.3f  false_eng=%.3f\n",
					lk["function_guess"], lk["task_type"], eval.FalseEngRate(rows, pred))
				fmt.Fprintf(out, "  s1_downstream_baseline=%.3f\n", eval.S1DownstreamBaseline(rows, pred))
				fmt.Fprintf(out, "  speech_act per-mood=%v\n", eval.SpeechActPerMood(rows, pred))
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&withContext, "context", false, "Feed session context (recent prompts, repo/branch) to the classifier.")
	cmd.Flags().BoolVar(&withConfound, "confound", false, "Include the confound set and report leakage/false-eng metrics.")
	return cmd
}
