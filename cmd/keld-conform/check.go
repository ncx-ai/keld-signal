package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ncx-ai/keld-signal/internal/conform/checkpoints"
)

func runCheck(args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	tool := fs.String("tool", "", "tool id, e.g. claude_code")
	chain := fs.String("chain", "A", "chain id")
	step := fs.String("step", "", "step name within the chain")
	seed := fs.String("seed", "", "the run's seed, echoed into the failure line")
	root := fs.String("transcript-root", "", "directory the tool writes transcripts under")
	since := fs.Int64("since", 0, "unix seconds; ignore transcripts older than this step")
	store := fs.String("store", "", "path to refseries.db")
	atlas := fs.String("mockatlas", "", "mock Atlas base URL")
	state := fs.String("mockatlas-state", "", "mock Atlas --state directory")
	skip := fs.String("not-expected", "", "comma-separated checkpoints this tool is not yet required to meet")
	if err := fs.Parse(args); err != nil {
		return err
	}

	expect := checkpoints.AllRequired()
	for _, name := range splitComma(*skip) {
		switch name {
		case checkpoints.Transcript:
			expect.Transcript = false
		case checkpoints.Pointer:
			expect.Pointer = false
		case checkpoints.StoreRows:
			expect.StoreRows = false
		case checkpoints.Telemetry:
			expect.Telemetry = false
		case checkpoints.Publish:
			expect.Publish = false
		default:
			return fmt.Errorf("unknown checkpoint %q in --not-expected", name)
		}
	}

	var cut time.Time
	if *since > 0 {
		cut = time.Unix(*since, 0)
	}

	rep := checkpoints.Check(checkpoints.Options{
		Tool: *tool, Chain: *chain, Step: *step, Seed: *seed,
		TranscriptRoot: *root, Since: cut,
		StorePath: *store, AtlasURL: *atlas, AtlasState: *state,
		Expect: expect,
	})

	fmt.Fprint(os.Stderr, rep.Summary())
	raw, _ := json.Marshal(rep)
	fmt.Println(string(raw))

	// ⚠️ The failure line is LAST, so a runner reads it with `tail -1`. It names
	// the first failed REQUIRED checkpoint and nothing else.
	if line := rep.FailureLine(); line != "" {
		fmt.Println(line)
		os.Exit(1)
	}
	return nil
}

func splitComma(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ',' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		if r == ' ' {
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}
