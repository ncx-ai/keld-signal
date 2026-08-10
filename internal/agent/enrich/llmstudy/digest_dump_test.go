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
	}
	type sess struct {
		Name      string `json:"name"`
		Synthetic bool   `json:"synthetic"`
		Windows   int    `json:"windows"`
		Steps     []step `json:"steps"`
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

		i0 := 3
		facts0 := FactsFrom(Extract(ws[i0]), ocs[:i0+1]).WithWindow(ws[i0]).WithPlace("", "", proj)
		win0 := Render(ws[i0])
		view0 := RenderSessionView(ws[i0])
		p0 := DigestCreatePromptWithView("work session", win0, view0, facts0.Block())
		d0, err := l.CreateDigestWithView("work session", win0, view0, facts0.Block())
		if err != nil {
			t.Fatalf("%s create: %v", src.label, err)
		}
		s.Steps = append(s.Steps, step{"create", view0, facts0.Block(), win0, p0, d0})

		i1 := len(ws) - 1
		facts1 := FactsFrom(Extract(ws[i1]), ocs[:i1+1]).WithWindow(ws[i1]).WithPlace("", "", proj)
		win1 := Render(ws[i1])
		view1 := RenderSessionView(ws[i1])
		p1 := DigestUpdatePromptWithView(d0, "work session", win1, view1, facts1.Block())
		d1, err := l.RefineDigestWithView(d0, "work session", win1, view1, facts1.Block())
		if err != nil {
			t.Fatalf("%s refine: %v", src.label, err)
		}
		s.Steps = append(s.Steps, step{"refine", view1, facts1.Block(), win1, p1, d1})

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
