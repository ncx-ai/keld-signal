//go:build llmstudy

package llmstudy

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestDigestShow prints one session's digest in FULL at every refinement step, so the
// report can be read as a reader would receive it. The sweep logs are clipped to 260
// characters, which is enough to spot a defect and not enough to judge usefulness —
// the one threshold (T5) that cannot be self-assessed.
//
//	DIGEST_SHOW=3 DIGEST_URL=http://127.0.0.1:8099 go test -tags llmstudy \
//	  ./internal/agent/enrich/llmstudy/ -run DigestShow -v -timeout 20m
func TestDigestShow(t *testing.T) {
	url := os.Getenv("DIGEST_URL")
	if url == "" {
		url = "http://127.0.0.1:8099"
	}
	want, _ := strconv.Atoi(os.Getenv("DIGEST_SHOW"))
	if want <= 0 {
		want = 1
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

	o := DefaultMineOpts()
	o.K = 12
	l := NewLlama(url)

	n := 0
	for _, f := range files {
		ws, e1 := Mine(f, o)
		ocs, e2 := Outcomes(f, o)
		if e1 != nil || e2 != nil || len(ws) < 16 || len(ws) != len(ocs) {
			continue
		}
		n++
		if n != want {
			continue
		}
		t.Logf("══════ session: %s (%d windows) ══════", filepath.Base(f), len(ws))

		var cur Digest
		for step, idx := range []int{4, 8, 12, 15} {
			w := ws[idx]
			facts := FactsFrom(Extract(w), ocs[:idx+1]).WithWindow(w).
				WithPlace("", "", projectFromPath(f))
			var d Digest
			var err error
			if step == 0 {
				d, err = l.CreateDigest("work session", Render(w), facts.Block())
			} else {
				d, err = l.RefineDigest(cur, "work session", Render(w), facts.Block())
			}
			if err != nil {
				t.Fatalf("step %d: %v", step, err)
			}
			cur = d
			t.Logf("\n\n╔═══ STEP %d (window %d of %d) ═══╗", step, idx, len(ws))
			t.Logf("FACTS GIVEN:\n%s", facts.Block())
			for _, s := range []struct{ k, v string }{
				{"DONE", d.Done}, {"HAPPENED", d.Happened}, {"STRUCTURE", d.Structure},
				{"CURRENT", d.Current}, {"WHY", d.Why}, {"NEXT", d.Next},
			} {
				t.Logf("── %s ──\n%s", s.k, s.v)
			}
			t.Logf("── INSIGHTS ──")
			for i, s := range d.Insights {
				t.Logf("  %d. %s", i+1, s)
			}
			t.Logf("── UNRESOLVED ──")
			for i, s := range d.Unresolved {
				t.Logf("  %d. %s", i+1, s)
			}
		}
		return
	}
	t.Skipf("fewer than %d qualifying sessions", want)
}
