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
	var withContext, withConfound, withCreds, withCalibration, withAgentic bool
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
			if withCreds {
				credRows, err := eval.LoadCreds()
				if err != nil {
					return err
				}
				credPred := eval.RunModelWithContext(model, credRows)
				fmt.Fprintf(out, "  %-15s secret_recall=%.3f  secret_fpr=%.3f\n", "creds",
					eval.SecretRecall(credRows, credPred), eval.SecretFPR(credRows, credPred))
			}
			if withCalibration {
				classifier := []string{"task_type", "domain", "activity_type", "speech_act", "subcategory"}
				ruleInfluenced := []string{"sensitivity", "function_guess"}
				printCal := func(title string, facets []string) {
					fmt.Fprintf(out, "\n== calibration: %s ==\n", title)
					for _, f := range facets {
						r := eval.Calibration(rows, pred, f, 10)
						fmt.Fprintf(out, "  %-15s N=%-3d ECE=%.3f\n", r.Facet, r.N, r.ECE)
						for _, b := range r.Bins {
							fmt.Fprintf(out, "      [%.1f,%.1f) n=%-3d conf=%.3f acc=%.3f\n", b.Lo, b.Hi, b.Count, b.MeanConf, b.Accuracy)
						}
					}
				}
				printCal("classifier facets", classifier)
				printCal("rule-influenced (confidence forced to 1.0 on some rows — reflects rules, not model)", ruleInfluenced)
			}
			if withAgentic {
				ag, err := eval.LoadAgentic()
				if err != nil {
					return err
				}
				aug := eval.RunModelWithContext(model, ag) // agentic Meta in the preamble
				bare := eval.RunModel(model, ag)           // no context
				fmt.Fprintf(out, "\n== agentic corpus (rows=%d) ==\n", len(ag))
				for _, f := range []string{"task_type", "domain"} {
					am := eval.Score(ag, aug, []string{f})[f]["accuracy"]
					bm := eval.Score(ag, bare, []string{f})[f]["accuracy"]
					fmt.Fprintf(out, "  %-10s augmented=%.3f  bare=%.3f  (Δ=%+.3f)\n", f, am, bm, am-bm)
					fmt.Fprintf(out, "      by shape (augmented): %v\n", eval.AccuracyByShape(ag, aug, f))
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&withContext, "context", false, "Feed session context (recent prompts, repo/branch) to the classifier.")
	cmd.Flags().BoolVar(&withConfound, "confound", false, "Include the confound set and report leakage/false-eng metrics.")
	cmd.Flags().BoolVar(&withCreds, "creds", false, "Score the credential-detection corpus and report secret_recall/secret_fpr.")
	cmd.Flags().BoolVar(&withCalibration, "calibration", false, "Print per-facet accuracy stratified by GLiNER2 confidence (reliability bins + ECE).")
	cmd.Flags().BoolVar(&withAgentic, "agentic", false, "Score the agentic-framework corpus: task_type/domain accuracy by shape and augmented-vs-bare.")
	return cmd
}
