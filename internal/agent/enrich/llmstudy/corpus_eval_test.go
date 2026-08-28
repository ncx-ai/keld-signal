package llmstudy

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestSmokeRealTranscripts is a temporary diagnostic, not a gate: it reports how
// the miner behaves on this machine's real transcripts. Skipped when none exist.
func TestSmokeRealTranscripts(t *testing.T) {
	if os.Getenv("LLMSTUDY_SMOKE") == "" {
		t.Skip("set LLMSTUDY_SMOKE=1 to run the real-transcript diagnostic")
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
	t.Logf("transcripts: %d", len(files))

	total, withCtx, toolTurns := 0, 0, 0
	var sample Window
	var lens []int
	for _, f := range files {
		ws, err := Mine(f, DefaultMineOpts())
		if err != nil {
			continue
		}
		for _, w := range ws {
			total++
			if len(w.Turns) > 1 {
				withCtx++
				lens = append(lens, len([]rune(Render(w))))
				for _, tn := range w.Turns {
					if tn.Role == RoleTool {
						toolTurns++
					}
				}
				if sample.PromptID == "" && len(w.Turns) >= 6 && len([]rune(w.Target)) > 25 {
					sample = w
				}
			}
		}
	}
	t.Logf("windows: %d total, %d with context, %d tool turns", total, withCtx, toolTurns)
	if len(lens) > 0 {
		sort.Ints(lens)
		t.Logf("rendered chars: p50=%d p95=%d max=%d", lens[len(lens)/2], lens[len(lens)*95/100], lens[len(lens)-1])
	}
	if sample.PromptID == "" {
		t.Log("no sample window found")
		return
	}
	r := Render(sample)
	if len([]rune(r)) > 1600 {
		r = string([]rune(r)[:1600]) + "\n…[truncated]"
	}
	t.Logf("\n--- SAMPLE (%d turns) target=%q ---\n%s", len(sample.Turns), sample.Target, r)
}
