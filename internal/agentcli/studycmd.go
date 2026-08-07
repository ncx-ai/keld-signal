package agentcli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/ncx-ai/keld-signal/internal/agent/enrich/lenstat"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/llmstudy"
	"github.com/ncx-ai/keld-signal/internal/agent/enrich/sidecar"
	"github.com/ncx-ai/keld-signal/internal/paths"
)

// studyDir holds the offline study's artifacts. Local only; nothing is published.
func studyDir() string { return filepath.Join(paths.KeldHome(), "study") }

func studyWriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o600)
}

func studyReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// newStudyCmd builds `keld-agent study`, the offline prompted-LLM vs GLiNER2
// harness. It is a measurement tool, not a product path: it reads transcripts
// locally, talks only to loopback backends, and publishes nothing.
func newStudyCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "study",
		Short: "Offline prompted-LLM vs GLiNER2 classification study (not a product path).",
		// Hidden from user-facing help: this is research tooling, not a feature
		// anyone installing the daemon should be offered. It stays compiled in
		// (measured at 108 KB, 0.75% of the stripped binary, and `keld-agent eval`
		// sets the precedent that eval tooling ships) but is inert unless invoked.
		Hidden: true,
	}
	c.AddCommand(newStudyMineCmd(), newStudyRunCmd(), newStudyAdjudicateCmd(),
		newStudyReportCmd(), newStudyPreviewCmd())
	return c
}

// newStudyPreviewCmd prints an arm's label distribution and a few worked examples,
// so a run can be inspected without reading raw JSON.
func newStudyPreviewCmd() *cobra.Command {
	var arm string
	var n int
	c := &cobra.Command{
		Use:   "preview",
		Short: "Show one arm's label distribution and sample predictions.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var ws []llmstudy.Window
			if err := studyReadJSON(filepath.Join(studyDir(), "windows.json"), &ws); err != nil {
				return err
			}
			var r llmstudy.Run
			if err := studyReadJSON(filepath.Join(studyDir(), "run-"+arm+".json"), &r); err != nil {
				return fmt.Errorf("read run for arm %q: %w", arm, err)
			}
			out := cmd.OutOrStdout()
			p50, p95, max := llmstudy.Latency(r)
			fmt.Fprintf(out, "arm=%s n=%d validity=%.3f partial=%.3f p50=%dms p95=%dms max=%dms\n\n",
				r.Arm, len(r.Answers), llmstudy.ValidityRate(r), llmstudy.PartialRate(r), p50, p95, max)

			facets := []llmstudy.Facet{
				llmstudy.FacetDomain, llmstudy.FacetTaskType, llmstudy.FacetFunction,
				llmstudy.FacetActivity, llmstudy.FacetPersonal, llmstudy.FacetSubcategory,
			}
			for _, f := range facets {
				counts := map[string]int{}
				for _, a := range r.Answers {
					if v := a.Labels[f]; v != "" {
						counts[v]++
					}
				}
				keys := make([]string, 0, len(counts))
				for k := range counts {
					keys = append(keys, k)
				}
				sort.Slice(keys, func(i, j int) bool {
					if counts[keys[i]] != counts[keys[j]] {
						return counts[keys[i]] > counts[keys[j]]
					}
					return keys[i] < keys[j]
				})
				var parts []string
				for i, k := range keys {
					if i == 6 {
						break
					}
					parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
				}
				fmt.Fprintf(out, "%-15s %2d distinct | %s\n", f, len(counts), strings.Join(parts, ", "))
			}

			fmt.Fprintf(out, "\n%s\nSAMPLE PREDICTIONS\n%s\n", strings.Repeat("=", 72), strings.Repeat("=", 72))
			for i := 0; i < len(r.Answers) && i < n; i++ {
				a := r.Answers[i]
				target := "?"
				turns := 0
				if i < len(ws) {
					target = strings.Join(strings.Fields(ws[i].Target), " ")
					if len(target) > 140 {
						target = target[:140] + "…"
					}
					turns = len(ws[i].Turns)
				}
				fmt.Fprintf(out, "\n[%d] %s\n", i, target)
				if !a.Valid {
					fmt.Fprintf(out, "    INVALID: %s\n", a.Err)
					continue
				}
				fmt.Fprintf(out, "    domain=%s task_type=%s function=%s subcat=%s\n",
					a.Labels[llmstudy.FacetDomain], a.Labels[llmstudy.FacetTaskType],
					a.Labels[llmstudy.FacetFunction], a.Labels[llmstudy.FacetSubcategory])
				fmt.Fprintf(out, "    activity=%s personal=%s latency=%dms turns=%d\n",
					a.Labels[llmstudy.FacetActivity], a.Labels[llmstudy.FacetPersonal],
					a.LatencyMS, turns)
			}
			return nil
		},
	}
	c.Flags().StringVar(&arm, "arm", "", "arm name to preview")
	c.Flags().IntVar(&n, "n", 8, "sample predictions to print")
	_ = c.MarkFlagRequired("arm")
	return c
}

func newStudyMineCmd() *cobra.Command {
	var root string
	var n, k, seed int
	c := &cobra.Command{
		Use:   "mine",
		Short: "Mine conversational windows from local transcripts.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if root == "" {
				home, err := os.UserHomeDir()
				if err != nil {
					return err
				}
				root = filepath.Join(home, ".claude", "projects")
			}
			var files []string
			err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return nil // tolerate unreadable subtrees
				}
				if !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
					files = append(files, p)
				}
				return nil
			})
			if err != nil {
				return err
			}
			sort.Strings(files) // deterministic order

			o := llmstudy.DefaultMineOpts()
			o.K = k
			var all []llmstudy.Window
			for _, f := range files {
				ws, err := llmstudy.Mine(f, o)
				if err != nil {
					continue
				}
				all = append(all, ws...)
			}
			picked := llmstudy.Sample(all, n, int64(seed))
			path := filepath.Join(studyDir(), "windows.json")
			if err := studyWriteJSON(path, picked); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"transcripts=%d windows=%d sampled=%d k=%d -> %s\n",
				len(files), len(all), len(picked), k, path)
			if len(picked) < n {
				fmt.Fprintf(cmd.OutOrStdout(),
					"NOTE: only %d eligible windows exist; N=%d was not reached\n", len(picked), n)
			}
			return nil
		},
	}
	c.Flags().StringVar(&root, "root", "", "transcript root (default ~/.claude/projects)")
	c.Flags().IntVar(&n, "n", 200, "windows to sample")
	c.Flags().IntVar(&k, "k", 8, "context turns before the target")
	c.Flags().IntVar(&seed, "seed", 7, "sampling seed")
	return c
}

func newStudyRunCmd() *cobra.Command {
	var arm, backend, kind string
	var limit int
	c := &cobra.Command{
		Use:   "run",
		Short: "Run one arm over the mined windows.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var ws []llmstudy.Window
			if err := studyReadJSON(filepath.Join(studyDir(), "windows.json"), &ws); err != nil {
				return fmt.Errorf("read windows (run `study mine` first): %w", err)
			}
			if limit > 0 && limit < len(ws) {
				ws = ws[:limit]
			}

			var cls func(llmstudy.Window) llmstudy.Answer
			switch kind {
			case "llm":
				cls = llmstudy.NewLlama(backend).Classify
			case "encoder":
				// Bind the SAME adaptive token cap the daemon binds
				// (daemon.go:246). Without it the control is not running as
				// shipped, and gliner2 applies no truncation at all — which
				// drove the sidecar worker to 45 mid-job hard kills on a first
				// attempt, turning the control's answers into failures.
				cap := lenstat.FromEnv(paths.PromptLengthsPath()).Cap()
				fmt.Fprintf(cmd.OutOrStdout(), "encoder arm: max_len=%d (production lenstat cap)\n", cap)
				cls = llmstudy.NewEncoderArm(sidecar.New(backend, 180*time.Second)).
					WithMaxLen(cap).Classify
			default:
				return fmt.Errorf("--kind must be llm or encoder, got %q", kind)
			}

			run := llmstudy.Run{Arm: arm, Answers: make([]llmstudy.Answer, 0, len(ws))}
			start := time.Now()
			for i, w := range ws {
				run.Answers = append(run.Answers, cls(w))
				if (i+1)%10 == 0 || i+1 == len(ws) {
					el := time.Since(start)
					fmt.Fprintf(cmd.OutOrStdout(), "  %s: %d/%d (%.0fs elapsed, %.1fs/window)\n",
						arm, i+1, len(ws), el.Seconds(), el.Seconds()/float64(i+1))
				}
			}
			p50, p95, max := llmstudy.Latency(run)
			path := filepath.Join(studyDir(), "run-"+arm+".json")
			if err := studyWriteJSON(path, run); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"%s: n=%d validity=%.3f partial=%.3f latency p50=%dms p95=%dms max=%dms -> %s\n",
				arm, len(run.Answers), llmstudy.ValidityRate(run), llmstudy.PartialRate(run),
				p50, p95, max, path)
			for _, a := range run.Answers {
				if a.Err != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "  first error: %s\n", a.Err)
					break
				}
			}
			return nil
		},
	}
	c.Flags().StringVar(&arm, "arm", "", "arm name (e.g. qwen3-4b, gliner2, guard-omni)")
	c.Flags().StringVar(&kind, "kind", "llm", "backend kind: llm (llama-server) or encoder (sidecar)")
	c.Flags().StringVar(&backend, "backend", "http://127.0.0.1:8080", "backend base URL")
	c.Flags().IntVar(&limit, "limit", 0, "only run the first N windows (0 = all)")
	_ = c.MarkFlagRequired("arm")
	return c
}

// loadRuns reads every run-*.json in the study dir, separating the control.
func loadRuns(control string) (llmstudy.Run, []llmstudy.Run, error) {
	entries, err := os.ReadDir(studyDir())
	if err != nil {
		return llmstudy.Run{}, nil, err
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "run-") && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // deterministic arm order
	var ctl llmstudy.Run
	var arms []llmstudy.Run
	for _, n := range names {
		var r llmstudy.Run
		if err := studyReadJSON(filepath.Join(studyDir(), n), &r); err != nil {
			return llmstudy.Run{}, nil, err
		}
		if r.Arm == control {
			ctl = r
			continue
		}
		arms = append(arms, r)
	}
	if ctl.Arm == "" {
		return llmstudy.Run{}, nil, fmt.Errorf("no run found for control arm %q", control)
	}
	return ctl, arms, nil
}

func newStudyAdjudicateCmd() *cobra.Command {
	var control string
	var seed int
	c := &cobra.Command{
		Use:   "adjudicate",
		Short: "Build the blinded adjudication set from arm disagreements.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var ws []llmstudy.Window
			if err := studyReadJSON(filepath.Join(studyDir(), "windows.json"), &ws); err != nil {
				return err
			}
			ctl, arms, err := loadRuns(control)
			if err != nil {
				return err
			}
			facets := []llmstudy.Facet{
				llmstudy.FacetDomain, llmstudy.FacetTaskType, llmstudy.FacetSubcategory,
				llmstudy.FacetFunction, llmstudy.FacetActivity,
			}
			set := llmstudy.Disagreements(ws, ctl, arms, facets, int64(seed))

			ip := filepath.Join(studyDir(), "items.json")
			pp := filepath.Join(studyDir(), "provenance.json")
			if err := studyWriteJSON(ip, set.Items); err != nil {
				return err
			}
			if err := studyWriteJSON(pp, set.Provenance); err != nil {
				return err
			}
			byFacet := map[string]int{}
			for _, it := range set.Items {
				byFacet[it.Facet]++
			}
			keys := make([]string, 0, len(byFacet))
			for k := range byFacet {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			fmt.Fprintf(cmd.OutOrStdout(), "%d disagreements across %d arms\n", len(set.Items), len(arms))
			for _, k := range keys {
				fmt.Fprintf(cmd.OutOrStdout(), "  %-15s %d\n", k, byFacet[k])
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"\nadjudicate -> %s  (fill each item's \"choice\" with an option key, \"tie\", or \"both_wrong\")\n"+
					"provenance -> %s  (do NOT open this while adjudicating)\n", ip, pp)
			return nil
		},
	}
	c.Flags().StringVar(&control, "control", "gliner2", "control arm name")
	c.Flags().IntVar(&seed, "seed", 7, "shuffle seed")
	return c
}

func newStudyReportCmd() *cobra.Command {
	var control string
	c := &cobra.Command{
		Use:   "report",
		Short: "Score adjudicated items into a results table.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var set llmstudy.AdjudicationSet
			if err := studyReadJSON(filepath.Join(studyDir(), "items.json"), &set.Items); err != nil {
				return err
			}
			if err := studyReadJSON(filepath.Join(studyDir(), "provenance.json"), &set.Provenance); err != nil {
				return err
			}
			decided := 0
			for _, it := range set.Items {
				if it.Choice != "" {
					decided++
				}
			}
			out := llmstudy.Markdown(llmstudy.Tallies(set, control))
			path := filepath.Join(studyDir(), "report.md")
			if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "adjudicated %d/%d items\n\n", decided, len(set.Items))
			fmt.Fprint(cmd.OutOrStdout(), out)
			fmt.Fprintf(cmd.OutOrStdout(), "\nwritten -> %s\n", path)
			if decided == 0 {
				fmt.Fprint(cmd.OutOrStdout(),
					"\nNothing adjudicated yet — fill in \"choice\" in items.json first.\n")
			}
			return nil
		},
	}
	c.Flags().StringVar(&control, "control", "gliner2", "control arm name")
	return c
}
