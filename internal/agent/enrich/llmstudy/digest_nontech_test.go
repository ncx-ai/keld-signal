//go:build llmstudy

package llmstudy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDigestNonTechnical runs the digest over HAND-AUTHORED non-engineering sessions.
//
// Why synthetic. The stated requirement is that this works for accountants and marketers
// as well as engineers, and the observed corpus cannot test it: all 14 project
// directories under ~/.claude/projects are code repositories, and Cowork — the surface
// non-engineers actually use — yields zero readable transcripts on VM-backed builds.
//
// What this can and cannot show. It CANNOT measure accuracy: the transcripts and the
// expectations are both mine, so agreement proves nothing about the real world. It CAN
// find structural failures, which is the actual risk — a pipeline that silently presumes
// code. Specifically: whether `structure` describes a PROCESS when there is no
// architecture, whether a tool profile of Read/Write means anything, whether the
// engineering vocabulary of the instructions bleeds into a finance report, and whether the
// reversal in each session is reported rather than smoothed over.
//
//	DIGEST_URL=http://127.0.0.1:8099 go test -tags llmstudy \
//	  ./internal/agent/enrich/llmstudy/ -run DigestNonTechnical -v -timeout 30m
func TestDigestNonTechnical(t *testing.T) {
	url := os.Getenv("DIGEST_URL")
	if url == "" {
		url = "http://127.0.0.1:8099"
	}
	files, err := filepath.Glob("testdata/nontech/*.jsonl")
	if err != nil || len(files) == 0 {
		t.Skip("no non-technical fixtures")
	}

	o := DefaultMineOpts()
	o.K = 12
	l := NewLlama(url)

	// Vocabulary that would betray the instructions leaking engineering framing into work
	// that has no code in it.
	codeWords := []string{"codebase", "repository", "refactor", "compile", "deploy",
		"API endpoint", "unit test", "merge conflict", "pull request"}

	for _, f := range files {
		ws, e1 := Mine(f, o)
		ocs, e2 := Outcomes(f, o)
		if e1 != nil || e2 != nil {
			t.Fatalf("%s: mine/outcomes: %v %v", f, e1, e2)
		}
		t.Logf("══════ %s: %d windows ══════", filepath.Base(f), len(ws))
		if len(ws) < 8 {
			t.Errorf("%s produced only %d windows; the miner may be dropping non-code turns", f, len(ws))
			continue
		}

		var cur Digest
		steps := []int{3, len(ws) / 2, len(ws) - 1}
		for step, idx := range steps {
			w := ws[idx]
			facts := FactsFrom(Extract(w), ocs[:idx+1]).WithWindow(w).
				WithPlace("", "", strings.TrimSuffix(filepath.Base(f), ".jsonl"))
			var d Digest
			var err error
			if step == 0 {
				d, err = l.CreateDigestWithView("work session", Render(w), RenderSessionView(w), facts.Block())
			} else {
				d, err = l.RefineDigestWithView(cur, "work session", Render(w), RenderSessionView(w), facts.Block())
			}
			if err != nil {
				t.Fatalf("%s step %d: %v", f, step, err)
			}
			cur = d
			if p := ValidateDigest(d); len(p) > 0 {
				t.Errorf("%s step %d malformed: %v", f, step, p)
			}
		}

		t.Logf("FACTS: %s", strings.ReplaceAll(strings.TrimSpace(
			FactsFrom(Extract(ws[len(ws)-1]), ocs).Block()), "\n", " | "))
		for _, s := range []struct{ k, v string }{
			{"DONE", cur.Done}, {"HAPPENED", cur.Happened}, {"STRUCTURE", cur.Structure},
			{"CURRENT", cur.Current}, {"WHY", cur.Why}, {"NEXT", cur.Next},
		} {
			t.Logf("── %s ──\n%s", s.k, s.v)
		}
		for i, s := range cur.Insights {
			t.Logf("  insight %d: %s", i+1, s)
		}
		for i, s := range cur.Unresolved {
			t.Logf("  unresolved %d: %s", i+1, s)
		}

		body := strings.ToLower(strings.Join([]string{cur.Done, cur.Happened,
			cur.Structure, cur.Current, cur.Why, cur.Next}, " "))
		for _, w := range codeWords {
			if strings.Contains(body, strings.ToLower(w)) {
				t.Errorf("%s: engineering vocabulary %q appeared in non-code work", f, w)
			}
		}
		src := Render(ws[len(ws)-1]) + "\n" + RenderSessionView(ws[len(ws)-1])
		if bad := UnverifiedIdentifiers(cur, src); len(bad) > 0 {
			t.Logf("  ⚠ unverified against the final window only: %v", bad)
		}
	}
}
