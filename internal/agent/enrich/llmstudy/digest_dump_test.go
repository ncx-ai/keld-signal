//go:build llmstudy

package llmstudy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDigestDump writes the COMPLETE input/output record for one create step and one
// refinement step: the facts block, the rendered window, the full prompt sent to the
// model, and the digest returned. Everything a reader needs to judge the prompts rather
// than take a summary of them on trust.
//
//	DIGEST_DUMP=/path/out.json DIGEST_URL=... go test -tags llmstudy \
//	  ./internal/agent/enrich/llmstudy/ -run DigestDump -v -timeout 30m
func TestDigestDump(t *testing.T) {
	out := os.Getenv("DIGEST_DUMP")
	if out == "" {
		t.Skip("set DIGEST_DUMP")
	}
	url := os.Getenv("DIGEST_URL")
	if url == "" {
		url = "http://127.0.0.1:8099"
	}

	type step struct {
		Kind   string `json:"kind"`
		View   string `json:"view"`
		Facts  string `json:"facts"`
		Window string `json:"window"`
		Prompt string `json:"prompt"`
		Digest Digest `json:"digest"`
		// The refine path no longer reads `facts` at all — SessionRecord replaced it — and it
		// reads a beat series that has no counterpart in the old dump. Both are recorded here
		// so the dump still shows EVERYTHING the model was given; a dump that omitted them
		// would show a reader a prompt they could not account for.
		Record string `json:"record,omitempty"`
		Beats  []Beat `json:"beats,omitempty"`
	}
	// One beat, with the prompt that produced it, so the cheap tier is judgeable on the same
	// terms as the expensive one rather than taken on trust.
	type beatDump struct {
		Ordinal int    `json:"ordinal"`
		Window  int    `json:"window"`
		Prompt  string `json:"prompt"`
		Text    string `json:"text"`
		Kept    bool   `json:"kept"`
	}
	type sess struct {
		Name      string     `json:"name"`
		Synthetic bool       `json:"synthetic"`
		Windows   int        `json:"windows"`
		Steps     []step     `json:"steps"`
		BeatDumps []beatDump `json:"beat_dumps,omitempty"`
	}

	var sources []struct {
		path      string
		label     string
		synthetic bool
	}
	// One hand-authored non-engineering session and one real engineering session.
	for _, f := range []string{"testdata/nontech/finance-close.jsonl", "testdata/nontech/marketing-launch.jsonl"} {
		sources = append(sources, struct {
			path      string
			label     string
			synthetic bool
		}{f, filepath.Base(f), true})
	}
	// This very session first: the longest and most correction-dense transcript here, and the
	// one whose report the reader can check from memory rather than trusting the harness.
	if me := ThisSessionTranscript(); me != "" {
		sources = append(sources, struct {
			path      string
			label     string
			synthetic bool
		}{me, "this-session", false})
	}
	real := StratifiedTranscripts()
	// One further real session from a DIFFERENT project, for contrast.
	for _, f := range real {
		if strings.Contains(f, "keld-atlas") {
			ws, e1 := Mine(f, DefaultMineOpts())
			if e1 != nil || len(ws) < 16 {
				continue
			}
			sources = append(sources, struct {
				path      string
				label     string
				synthetic bool
			}{f, filepath.Base(f), false})
			break
		}
	}

	o := DefaultMineOpts()
	o.K = 12
	l := NewLlama(url)
	var all []sess

	for _, src := range sources {
		ws, e1 := Mine(src.path, o)
		ocs, e2 := Outcomes(src.path, o)
		if e1 != nil || e2 != nil || len(ws) < 8 {
			t.Logf("skip %s: %v %v", src.label, e1, e2)
			continue
		}
		s := sess{Name: src.label, Synthetic: src.synthetic, Windows: len(ws)}
		proj := strings.TrimSuffix(filepath.Base(src.path), ".jsonl")
		if !src.synthetic {
			proj = projectFromPath(src.path)
		}

		// The record is accumulated from DISJOINT per-user-prompt deltas, not from Mine's
		// overlapping windows — see sessionDeltas in digest_eval_test.go. A dump built the
		// other way would show a reader an authoritative counts block that does not describe
		// the session.
		deltas, e3 := sessionDeltas(src.path, o)
		if e3 != nil || len(deltas) != len(ws) {
			t.Logf("skip %s: deltas %v (%d vs %d)", src.label, e3, len(deltas), len(ws))
			continue
		}
		rec := SessionRecord{}
		var beats []Beat
		beatTurns := BeatTurnsFromEnv()
		// observe folds every window up to and including `upto` into the record, generating
		// beats on the way at the same cadence the sweep uses, so the record and series a
		// dumped prompt carries are the ones that prompt would really have.
		observe := func(from, upto int) {
			for i := from; i <= upto && i < len(ws); i++ {
				rec = rec.Observe(deltas[i], Extract(deltas[i])).WithProject(proj)
				if (i+1)%beatTurns != 0 {
					continue
				}
				bp := BeatPrompt(rec.Block(), Render(ws[i]))
				text, err := l.GenerateBeat(rec.Block(), Render(ws[i]))
				if err != nil {
					t.Logf("%s beat at window %d: %v", src.label, i, err)
					continue
				}
				var kept bool
				beats, kept = AppendBeat(beats, text)
				s.BeatDumps = append(s.BeatDumps, beatDump{
					Ordinal: len(beats), Window: i, Prompt: bp, Text: text, Kept: kept,
				})
			}
		}

		i0 := 3
		observe(0, i0)
		facts0 := FactsFrom(Extract(ws[i0]), ocs[:i0+1]).WithWindow(ws[i0]).WithPlace("", "", proj)
		win0 := Render(ws[i0])
		view0 := RenderSessionView(ws[i0])
		p0 := DigestCreatePromptWithView("work session", win0, view0, facts0.Block())
		d0, err := l.CreateDigestWithView("work session", win0, view0, facts0.Block())
		if err != nil {
			t.Fatalf("%s create: %v", src.label, err)
		}
		s.Steps = append(s.Steps, step{Kind: "create", View: view0, Facts: facts0.Block(),
			Window: win0, Prompt: p0, Digest: d0})

		// Production refreshes at most every MaxTurns user turns, so a 44-window session gets
		// several refinements, not one. Stepping through them is what lets a synopsis
		// re-scope; a single leap from window 3 to window 43 cannot.
		mid := []int{}
		for i := i0 + DefaultTriggerPolicy().MaxTurns; i < len(ws)-1; i += DefaultTriggerPolicy().MaxTurns {
			mid = append(mid, i)
		}
		cur := d0
		prev := i0
		for _, mi := range mid {
			observe(prev+1, mi)
			prev = mi
			nx, err := l.RefineFrom(cur, RefineInput{
				SessionLabel: "work session", Record: rec, Beats: beats,
				SessionView: RenderSessionView(ws[mi]), NewTurns: Render(ws[mi]),
				Why: TriggerFocusShift,
			})
			if err != nil {
				t.Fatalf("%s mid-refine: %v", src.label, err)
			}
			cur = nx
			rec = rec.NoteTurningPoint(len(s.Steps), TriggerFocusShift)
		}

		i1 := len(ws) - 1
		observe(prev+1, i1)
		facts1 := FactsFrom(Extract(ws[i1]), ocs[:i1+1]).WithWindow(ws[i1]).WithPlace("", "", proj)
		win1 := Render(ws[i1])
		view1 := RenderSessionView(ws[i1])
		in1 := RefineInput{
			SessionLabel: "work session", Record: rec, Beats: beats,
			SessionView: view1, NewTurns: win1, Why: TriggerFocusShift,
		}
		p1 := DigestUpdatePromptFrom(cur, in1)
		d1, err := l.RefineFrom(cur, in1)
		if err != nil {
			t.Fatalf("%s refine: %v", src.label, err)
		}
		// Facts is still recorded for the refine step even though the refine prompt no longer
		// embeds it: it is what the harness SCORES against (T3's correction gate, T7's clean-run
		// gate), so a reader checking a threshold needs it beside the prompt that was sent.
		s.Steps = append(s.Steps, step{Kind: "refine", View: view1, Facts: facts1.Block(),
			Window: win1, Prompt: p1, Digest: d1, Record: rec.Block(), Beats: beats})

		all = append(all, s)
		t.Logf("dumped %s (%d windows)", src.label, len(ws))
	}

	blob, _ := json.MarshalIndent(map[string]any{
		"sessions": all,
		"schema":   DigestUpdateSchema(),
		"sections": digestSections,
		"rules":    digestRules,
		"model":    "Qwen3-4B-Instruct-2507 Q4_K_M",
		"caps":     map[string]int{"prose": DefaultProseCap, "structure": DefaultStructureCap, "happened": DefaultHappenedCap, "list": DefaultListCap, "promptChars": DefaultPromptCharBudget},
	}, "", "  ")
	if err := os.WriteFile(out, blob, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %s (%d bytes)", out, len(blob))
}
