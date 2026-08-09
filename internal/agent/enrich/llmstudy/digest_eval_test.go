//go:build llmstudy

// Live digest harness. Requires a llama-server.
//
//	DIGEST_URL=http://127.0.0.1:8095 go test -tags llmstudy \
//	  ./internal/agent/enrich/llmstudy/ -run DigestSizing -v -timeout 60m
package llmstudy

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// projectFromPath recovers a readable project name from a Claude Code transcript
// path, whose parent directory encodes the working directory (e.g.
// "-home-dg-keld-keld-signal"). Gives the digest a real "working in" anchor without
// inventing one.
func projectFromPath(p string) string {
	d := filepath.Base(filepath.Dir(p))
	d = strings.TrimPrefix(d, "-")
	parts := strings.Split(d, "-")
	if len(parts) == 0 {
		return ""
	}
	return parts[len(parts)-1]
}

// TestDigestSizing is verification test 6: can a budget-fitting model write a usable
// digest at all? Free generation is the capability class where Qwen3-0.6B collapsed
// to a single value, so this must be answered before any prompt tuning — tuning
// against the wrong model is wasted work.
func TestDigestSizing(t *testing.T) {
	url := os.Getenv("DIGEST_URL")
	if url == "" {
		url = "http://127.0.0.1:8095"
	}
	root := filepath.Join(os.Getenv("HOME"), ".claude", "projects")
	var files []string
	filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	if len(files) == 0 {
		t.Skip("no transcripts")
	}

	o := DefaultMineOpts()
	o.K = 12 // wider than classification uses: a digest needs more material
	l := NewLlama(url)

	tried, ok := 0, 0
	for _, f := range files {
		if tried >= 4 {
			break
		}
		ws, err := Mine(f, o)
		if err != nil || len(ws) < 8 {
			continue
		}
		ocs, err := Outcomes(f, o)
		if err != nil || len(ocs) != len(ws) {
			continue
		}
		idx := len(ws) - 1
		w := ws[idx]
		facts := FactsFrom(Extract(w), ocs[:idx+1]).
			WithWindow(w).
			WithPlace("", "", projectFromPath(f))
		tried++

		d, err := l.CreateDigest("work session", Render(w), facts.Block())
		if err != nil {
			t.Errorf("[%d] call failed: %v", tried, err)
			continue
		}
		problems := ValidateDigest(d)
		if len(problems) > 0 {
			t.Errorf("[%d] malformed: %v", tried, problems)
			continue
		}
		ok++
		t.Logf("═══ digest %d — %s ═══", tried, filepath.Base(f))
		t.Logf("  FACTS GIVEN: %s", strings.ReplaceAll(strings.TrimSpace(facts.Block()), "\n", " | "))
		t.Logf("  done:       %s", clipLog(d.Done))
		t.Logf("  happened:   %s", clipLog(d.Happened))
		t.Logf("  structure:  %s", clipLog(d.Structure))
		t.Logf("  current:    %s", clipLog(d.Current))
		t.Logf("  why:        %s", clipLog(d.Why))
		t.Logf("  next:       %s", clipLog(d.Next))
		for i, s := range d.Insights {
			t.Logf("  insight[%d]: %s", i, clipLog(s))
		}
		for i, s := range d.Unresolved {
			t.Logf("  unresolved[%d]: %s", i, clipLog(s))
		}
		// Threshold 2, per digest: specifics must occur in the source.
		if bad := UnverifiedIdentifiers(d, Render(w)); len(bad) > 0 {
			t.Logf("  ⚠ unverified specifics: %v", bad)
		}
	}
	t.Logf("structural validity: %d/%d", ok, tried)
	if tried > 0 && ok != tried {
		t.Errorf("threshold 1 requires 100%% structural validity, got %d/%d", ok, tried)
	}
}

func clipLog(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 260 {
		return s[:260] + "…"
	}
	return s
}
